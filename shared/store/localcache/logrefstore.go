// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package localcache

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"

	"github.com/google/abfs/shared/entities"

	"golang.org/x/sync/errgroup"
)

type LogRefStore struct {
	dir           string
	mu            sync.RWMutex
	servers       map[string]*serverRefs
	setupComplete bool
	setupErr      error
	setupMu       sync.RWMutex
}

func NewLogRefStore(dir string) (*LogRefStore, error) {
	if err := os.MkdirAll(dir, cacheDirsUmask); err != nil {
		return nil, err
	}
	files, err := getFiles(dir)
	if err != nil {
		return nil, err
	}

	s := &LogRefStore{
		dir:           dir,
		servers:       make(map[string]*serverRefs, len(files)),
		setupComplete: false,
	}

	for _, fn := range files {
		s.servers[fn] = newServerRefs()
	}

	s.setupMu.Lock()
	go func() {
		defer s.setupMu.Unlock()
		var errs errgroup.Group
		for fn, server := range s.servers {
			fn, server := fn, server
			errs.Go(
				func() error {
					return server.open(path.Join(dir, fn))
				},
			)
		}
		if err := errs.Wait(); err != nil {
			if err := s.Close(); err != nil {
				log.Printf("error closing log ref store: %v", err)
			}
			s.setupErr = fmt.Errorf("error initializing log ref store so all ref requests will fail, error was: %w", err)
			return
		}
		s.setupComplete = true
	}()

	return s, nil
}

// ensureSetup must be called before any operation to ensure we're set up
func (s *LogRefStore) ensureSetup() error {
	if !s.setupComplete {
		s.setupMu.RLock()
		defer s.setupMu.RUnlock()
		if s.setupErr != nil {
			return s.setupErr
		}
	}
	return nil
}

func (s *LogRefStore) Close() error {
	// potentially a race condition if we aren't done setting up, but we don't
	// want to block on a long setup here if the user is trying to exit
	errs := make([]error, 0, len(s.servers))
	for _, server := range s.servers {
		if server.file != nil {
			errs = append(errs, server.file.Close())
		}
	}
	return errors.Join(errs...)
}

func getFiles(dir string) ([]string, error) {
	entities, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	result := make([]string, 0, len(entities))
	for _, ent := range entities {
		if ent.IsDir() {
			continue
		}
		result = append(result, ent.Name())
	}
	return result, nil
}

func checkScheme(server string) error {
	if strings.Contains(server, "://") {
		return fmt.Errorf("cannot accept server with a scheme, detected %q in %v", "://", server)
	}

	return nil
}

// Removes linux path separators.
func cleanServerPath(server string) string {
	return strings.ReplaceAll(server, string(os.PathSeparator), "_")
}

func (s *LogRefStore) Put(server string, project string, refs []entities.RefHash) error {
	if err := s.ensureSetup(); err != nil {
		return err
	}
	if err := checkScheme(server); err != nil {
		return err
	}
	cleaned := cleanServerPath(server)
	project = strings.ReplaceAll(project, "/", "@")
	sRefs, err := s.getOrCreateServer(cleaned)
	if err != nil {
		return err
	}
	for _, rh := range refs {
		if err := sRefs.put(project, rh.Ref, rh.Hash); err != nil {
			return err
		}
	}
	return nil
}

func (s *LogRefStore) Get(server string, project string, refs []string) ([]entities.Hash, error) {
	if err := s.ensureSetup(); err != nil {
		return nil, err
	}
	if err := checkScheme(server); err != nil {
		return nil, err
	}
	cleaned := cleanServerPath(server)
	project = strings.ReplaceAll(project, "/", "@")
	s.mu.RLock()
	r, ok := s.servers[cleaned]
	s.mu.RUnlock()
	if !ok {
		return nil, syscall.ENOENT
	}
	result := make([]entities.Hash, len(refs))
	for i, ref := range refs {
		result[i] = r.refs.Get(path.Join(ref, project))
	}
	return result, nil
}

func (s *LogRefStore) Search(server string, project string, refPrefix string) ([]entities.ProjectRefHash, error) {
	if err := s.ensureSetup(); err != nil {
		return nil, err
	}
	if err := checkScheme(server); err != nil {
		return nil, err
	}
	cleaned := cleanServerPath(server)
	s.mu.RLock()
	r, ok := s.servers[cleaned]
	s.mu.RUnlock()
	if !ok {
		return nil, syscall.ENOENT
	}
	origProject := project
	project = strings.ReplaceAll(project, "/", "@")

	var result []entities.ProjectRefHash
	if project != "" {
		prefix := path.Join(project, refPrefix)
		for r, hash := range r.projects.GetChildren(prefix) {
			if hash == entities.ZeroHash {
				continue
			}
			ref := strings.TrimPrefix(r, project+"/")
			result = append(result, entities.ProjectRefHash{Project: origProject, Ref: ref, Hash: hash})
		}
		return result, nil
	}

	for r, hash := range r.refs.GetChildren(refPrefix) {
		if hash == entities.ZeroHash {
			continue
		}
		ref, project := path.Split(r)
		project = strings.ReplaceAll(project, "@", "/")
		result = append(result, entities.ProjectRefHash{Project: project, Ref: strings.Trim(ref, "/"), Hash: hash})
	}
	return result, nil
}

func (s *LogRefStore) getOrCreateServer(cleanedServer string) (*serverRefs, error) {
	s.mu.RLock()
	r, ok := s.servers[cleanedServer]
	s.mu.RUnlock()
	if ok {
		return r, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if r, ok := s.servers[cleanedServer]; ok {
		return r, nil
	}
	r = newServerRefs()
	if err := r.open(path.Join(s.dir, cleanedServer)); err != nil {
		return nil, err
	}
	s.servers[cleanedServer] = r
	return r, nil
}

type serverRefs struct {
	file     *os.File
	projects *Folder
	refs     *Folder
}

func newServerRefs() *serverRefs {
	return &serverRefs{
		projects: NewFolder(),
		refs:     NewFolder(),
	}
}

func (s *serverRefs) open(fileName string) error {
	f, err := os.OpenFile(fileName, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err := s.load(f); err != nil {
		_ = f.Close()
		return err
	}
	s.file = f
	return nil
}

func (s *serverRefs) load(reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	for lineNum := 1; scanner.Scan(); lineNum++ {
		line := strings.TrimSpace(scanner.Text())
		fields := strings.SplitN(line, " ", 3)
		if len(fields) < 3 || fields[2] == "" {
			return fmt.Errorf("unexpected number of fields in line %d", lineNum)
		}
		project, hashHex, ref := fields[0], fields[1], fields[2]

		var hash entities.Hash
		if n, err := hex.Decode(hash[:], []byte(hashHex)); err != nil || n != 20 {
			return fmt.Errorf("expected a hash in line %d", lineNum)
		}
		if err := s.addRef(project, ref, hash); err != nil {
			return fmt.Errorf("cannot load line %d: %w", lineNum, err)
		}
	}
	return scanner.Err()
}

func (s *serverRefs) addRef(project, ref string, hash entities.Hash) error {
	if err := s.projects.Add(path.Join(project, ref), hash); err != nil {
		return err
	}
	if err := s.refs.Add(path.Join(ref, project), hash); err != nil {
		return err
	}
	return nil
}

func (s *serverRefs) put(project, ref string, hash entities.Hash) error {
	if err := s.addRef(project, ref, hash); err != nil {
		return err
	}
	_, err := fmt.Fprintf(s.file, "%s %s %s\n", project, hash, ref)
	return err
}
