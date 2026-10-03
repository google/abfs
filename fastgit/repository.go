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

package fastgit

import (
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/shared/entities"
)

type Hash = entities.Hash

type Repository struct {
	path string

	packs []*Pack
}

func Open(repoPath string) (*Repository, error) {
	if _, err := os.Stat(repoPath + "/"); err != nil {
		return nil, err
	}
	r := &Repository{
		path: repoPath,
	}
	packs, err := r.detectPacks()
	if err != nil {
		return nil, err
	}
	r.packs = packs
	return r, nil
}

func (r *Repository) Close() error {
	for _, pack := range r.packs {
		if err := pack.Close(); err != nil {
			log.Println(err)
		}
	}
	return nil
}

func (r *Repository) detectPacks() ([]*Pack, error) {
	packPaths, err := filepath.Glob(filepath.Join(r.path, "objects/pack/*.idx"))
	if err != nil {
		return nil, err
	}

	packs := make([]*Pack, len(packPaths))
	group, ctx := errgroup.WithContext(context.Background())
	group.SetLimit(runtime.NumCPU())
	for i, pp := range packPaths {
		group.Go(func() error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			pack, err := openPack(strings.TrimSuffix(pp, ".idx"))
			if err != nil {
				return fmt.Errorf("error processing '%s': %w", pp, err)
			}
			packs[i] = pack
			return nil
		})
	}
	return packs, group.Wait()
}

func (r *Repository) OpenObject(oid Hash) (io.ReadCloser, error) {
	for _, pack := range r.packs {
		if r, err := pack.OpenObject(oid); !entities.IsNotExist(err) {
			return r, nil
		}
	}
	return r.openLooseObject(oid)
}

func (r *Repository) loosePath(oid Hash) string {
	hex := oid.String()
	return filepath.Join(r.path, "objects", hex[:2], hex[2:])
}

func (r *Repository) openLooseObject(oid Hash) (io.ReadCloser, error) {
	path := r.loosePath(oid)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	reader, err := zlib.NewReader(file)
	if err != nil {
		if err := file.Close(); err != nil {
			log.Printf("failed to close file %q: %v", path, err)
		}
		return nil, fmt.Errorf("zlib.NewReader: %w", err)
	}

	return &struct {
		io.Reader
		io.Closer
	}{
		Reader: reader,
		Closer: multiCloser{reader, file},
	}, nil
}

type multiCloser []io.Closer

func (m multiCloser) Close() error {
	var err error
	for _, closer := range m {
		if closeErr := closer.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	return err
}
