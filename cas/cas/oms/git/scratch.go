// Copyright 2025 Google LLC
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

package git

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	ScratchDirMask = 0xFF
)

type Scratch struct {
	Dir       string
	Inodes    [ScratchDirMask + 1]string
	Exclusive bool
}

func NewScratch(dir string, excl bool) *Scratch {
	return &Scratch{Dir: dir, Exclusive: excl}
}
func (s *Scratch) Init(basePath string) error {

	var err error
	// Populate our full Dir path based on input basePath
	// if no Scratch.Dir was configured create dynamic one
	if s.Dir == "" {
		// Create parent directory as needed
		err = os.MkdirAll(basePath, cacheDirsUmask)
		if err != nil {
			return err
		}
		s.Dir, err = os.MkdirTemp(basePath, "rnd")
		if err != nil {
			return err
		}
		err = os.Chmod(s.Dir, cacheDirsUmask)
		if err != nil {
			return err
		}

	} else {
		s.Dir = path.Join(basePath, s.Dir)
		// Create parent directories as needed
		pDir := filepath.Dir(s.Dir)
		if err := os.MkdirAll(pDir, cacheDirsUmask); err != nil {
			return err
		}

		err := os.Mkdir(s.Dir, cacheDirsUmask)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				if s.Exclusive {
					return fmt.Errorf("scratch directory <%s> exists", s.Dir)
				} else {
					err = nil
				}
			}

			if err != nil {
				return err
			}
		}
	}

	for i := range ScratchDirMask + 1 {
		dirName := path.Join(s.Dir, fmt.Sprintf("%d", i))
		if err := os.MkdirAll(dirName, cacheDirsUmask); err != nil {
			return err
		}
		s.Inodes[i] = dirName
	}
	return nil
}
func (s *Scratch) Cleanup() error {
	if !s.Exclusive {
		return nil
	}

	if s.Dir == "" || s.Dir == "/" {
		return fmt.Errorf("refusing to cleanup invalid or root scratch directory paths")
	}

	// os.RemoveAll is efficient and handles all subdirectories (0-255) and nested files
	err := os.RemoveAll(s.Dir)
	if err != nil {
		return fmt.Errorf("failed to clean up scratch directory %s: %w", s.Dir, err)
	}

	return nil
}
func (s *Scratch) Remove(f string) error {
	return os.Remove(f)
}
func (s *Scratch) Create() (string, int, error) {
	return allocPath(s.Dir)
}
func (s *Scratch) CreateInum(inum int) (string, int, error) {
	bucket := inum & ScratchDirMask
	name := path.Join(s.Inodes[bucket], fmt.Sprintf("%d", inum))
	fd, err := syscall.Open(name, syscall.O_CREAT|syscall.O_RDWR|syscall.O_EXCL|syscall.O_TRUNC, 0600)
	return name, fd, err
}

var randomLock sync.Mutex
var random = rand.New(rand.NewSource(time.Now().Unix()))

func allocPath(baseDir string) (string, int, error) {

	var name string
	for {
		rnd := nextRandom()
		name = fmt.Sprintf("%s/%d", baseDir, rnd)
		fd, err := syscall.Open(name, syscall.O_CREAT|syscall.O_RDWR|syscall.O_EXCL, 0600)

		if err == nil {
			return name, fd, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return name, -1, err
		}
	}
}
func nextRandom() int {
	randomLock.Lock()
	defer randomLock.Unlock()
	return random.Int()
}
