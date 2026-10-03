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
	"strings"
	"sync"
	"syscall"

	"github.com/google/abfs/shared/entities"
)

type Folder struct {
	mu      sync.RWMutex
	hashes  map[string]entities.Hash
	folders map[string]*Folder
}

func NewFolder() *Folder {
	return &Folder{
		hashes:  make(map[string]entities.Hash),
		folders: make(map[string]*Folder),
	}
}

func (f *Folder) Add(p string, hash entities.Hash) error {
	pos := strings.IndexByte(p, '/')
	if pos < 0 {
		return f.addHash(p, hash)
	}
	dn := p[:pos]
	child := f.getFolder(dn)
	if child == nil {
		if err := f.addFolder(dn); err != nil {
			return err
		}
		child = f.getFolder(dn)
	}
	return child.Add(p[pos+1:], hash)
}

func (f *Folder) Get(p string) entities.Hash {
	pos := strings.IndexByte(p, '/')
	if pos < 0 {
		f.mu.RLock()
		defer f.mu.RUnlock()

		return f.hashes[p]
	}
	child := f.getFolder(p[:pos])
	if child == nil {
		return entities.Hash{}
	}

	return child.Get(p[pos+1:])
}

func (f *Folder) get(p string) (entities.Hash, *Folder) {
	if p == "" {
		return entities.Hash{}, f
	}
	pos := strings.IndexByte(p, '/')
	if pos < 0 {
		f.mu.RLock()
		defer f.mu.RUnlock()

		if h, ok := f.hashes[p]; ok {
			return h, nil
		} else if c, ok := f.folders[p]; ok {
			return entities.Hash{}, c
		} else {
			return entities.Hash{}, nil
		}
	}
	child := f.getFolder(p[:pos])
	if child == nil {
		return entities.Hash{}, nil
	}

	return child.get(p[pos+1:])
}

func (f *Folder) GetChildren(p string) map[string]entities.Hash {
	result := make(map[string]entities.Hash)
	h, c := f.get(p)
	if h != entities.ZeroHash {
		result[p] = h
		return result
	}
	if c == nil {
		return result
	}
	if p != "" {
		p += "/"
	}
	c.addChildren(p, result)
	return result
}

func (f *Folder) addChildren(append string, result map[string]entities.Hash) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	for n, h := range f.hashes {
		result[append+n] = h
	}

	for n, c := range f.folders {
		c.addChildren(append+n+"/", result)
	}
}

func (f *Folder) getFolder(p string) *Folder {
	f.mu.RLock()
	defer f.mu.RUnlock()

	return f.folders[p]
}

func (f *Folder) addHash(p string, hash entities.Hash) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.folders[p]; ok {
		return syscall.EISDIR
	}

	f.hashes[p] = hash
	return nil
}

func (f *Folder) addFolder(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.hashes[p]; ok {
		return syscall.ENOTDIR
	}

	if _, ok := f.folders[p]; !ok {
		f.folders[p] = NewFolder()
	}
	return nil
}
