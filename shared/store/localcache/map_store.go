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
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/abfs/shared/entities"
)

type MapStore struct {
	dir    string
	levels int
}

func NewMapStore(dir string, levels int) (*MapStore, error) {
	if err := os.MkdirAll(dir, cacheDirsUmask); err != nil {
		return nil, err
	}

	if levels != 2 && levels != 3 {
		return nil, fmt.Errorf("number of levels not supported: %d", levels)
	}

	return &MapStore{
		dir:    dir,
		levels: levels,
	}, nil
}

// Path returns the local path from where the file can be read.
func (c *MapStore) path(id entities.Hash) string {
	str := id.String()
	switch c.levels {
	case 2:
		return fmt.Sprintf("%s/%s/%s", c.dir, str[:3], str[3:])
	case 3:
		return fmt.Sprintf("%s/%s/%s/%s", c.dir, str[:3], str[3:6], str[6:])
	default:
		panic("unexpected levels value")
	}
}

func (c *MapStore) MapResults(id entities.Hash) (hashes []entities.Hash, retErr error) {
	var reader io.ReadCloser
	reader, err := os.Open(c.path(id))
	if entities.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	defer entities.CloseErrCheck(reader, &retErr)
	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	hexes := bytes.Split(content, []byte{'\n'})
	hashes = make([]entities.Hash, 0, len(hexes))
	for _, hex := range hexes {
		if len(hex) > 0 {
			hashes = append(hashes, entities.NewHash(string(hex)))
		}
	}
	return hashes, err
}

// AddMapResult is NOT safe to be run concurrently
func (c *MapStore) AddMapResult(from, to entities.Hash) (err error) {
	hashes, err := c.MapResults(from)
	if err != nil {
		return err
	}

	for _, h := range hashes {
		if h == to {
			return nil
		}
	}

	p := c.path(from)
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, cacheDirsUmask); err != nil {
		return err
	}

	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil
	}

	defer func() {
		if err1 := f.Close(); err == nil && err1 != nil {
			err = err1
		}
	}()

	if _, err := fmt.Fprintln(f, to.String()); err != nil {
		return err
	}
	return nil
}
