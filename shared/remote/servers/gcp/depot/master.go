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

package depot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/abfs/shared/remote"
)

const cleanupTimeout = 10 * time.Second

func NewMasterDepot(depots []Depot) *MasterDepot {
	if len(depots) == 0 {
		panic("cannot create master-depot with no inner depots")
	}
	return &MasterDepot{depots: depots}
}

type MasterDepot struct {
	depots []Depot
}

func (d *MasterDepot) Store(ctx context.Context, namePrefix string, r io.Reader) (string, error) {
	chunks, err := d.StoreChunks(ctx, namePrefix, r)
	if err != nil {
		return "", err
	}
	contentDesc, err := json.Marshal(chunks)
	return string(contentDesc), err
}

func (d *MasterDepot) StoreChunks(ctx context.Context, namePrefix string, r io.Reader) (chunks []Chunk, err error) {
	defer func() {
		if err != nil && len(chunks) > 0 {
			toRemove := chunks
			chunks = nil
			go func() {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
				defer cancel()
				_ = d.RemoveChunks(cleanupCtx, toRemove)
			}()
		}
	}()

	chunks = []Chunk{}
	var s Chunk
	for pi := 0; err != io.EOF; pi = min(pi+1, len(d.depots)-1) {
		depot := d.depots[pi]
		chunkName := fmt.Sprintf("%s-%d", namePrefix, len(chunks))
		s, err = depot.Store(ctx, chunkName, r)
		var tooBigErr *errTooBig
		if errors.As(err, &tooBigErr) {
			r = io.MultiReader(tooBigErr.alreadyRead, r)
			continue
		}
		if err != nil && err != io.EOF {
			return chunks, err
		}
		if s.Size == 0 {
			break
		}
		chunks = append(chunks, s)
	}
	return chunks, nil
}

func (d *MasterDepot) Load(ctx context.Context, contentDesc string) (io.ReadCloser, error) {
	var chunks []Chunk
	if err := json.Unmarshal([]byte(contentDesc), &chunks); err != nil {
		return nil, err
	}
	return d.LoadChunks(ctx, chunks)
}

type multiReader struct {
	ctx     context.Context
	depots  []Depot
	chunks  []Chunk
	current io.ReadCloser
	index   int
}

// Read implements io.ReadCloser.
func (m *multiReader) Read(p []byte) (int, error) {
	if m.current == nil {
		if m.index >= len(m.chunks) {
			return 0, io.EOF
		}
		reader, err := m.depots[m.index].Load(m.ctx, m.chunks[m.index])
		if err != nil {
			return 0, err
		}
		m.current = reader
	}
	n, err := m.current.Read(p)
	if err == io.EOF {
		err = nil
		if err := m.current.Close(); err != nil {
			return n, err
		}
		m.current = nil
		m.index++
	}
	return n, err
}

// Close implements io.ReadCloser.
func (m *multiReader) Close() error {
	if m.current == nil {
		return nil
	}
	err := m.current.Close()
	m.current = nil
	return err
}

func (d *MasterDepot) LoadChunks(ctx context.Context, chunks []Chunk) (io.ReadCloser, error) {
	depots := make([]Depot, len(chunks))
	for i, s := range chunks {
		depots[i] = d.getDepot(s.Depot)
		if depots[i] == nil {
			return nil, fmt.Errorf("cannot find depot %q", s.Depot)
		}
	}

	return &multiReader{
		ctx:    ctx,
		depots: depots,
		chunks: chunks,
	}, nil
}

func (d *MasterDepot) Remove(ctx context.Context, contentDesc string) error {
	var chunks []Chunk
	if err := json.Unmarshal([]byte(contentDesc), &chunks); err != nil {
		return err
	}
	return d.RemoveChunks(ctx, chunks)
}

func (d *MasterDepot) RemoveChunks(ctx context.Context, chunks []Chunk) error {
	var errs []error
	for _, s := range chunks {
		depot := d.getDepot(s.Depot)
		if depot == nil {
			errs = append(errs, fmt.Errorf("cannot find depot %q", s.Depot))
			continue
		}
		if err := depot.Remove(ctx, s); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (d *MasterDepot) getDepot(depotName string) Depot {
	for _, depot := range d.depots {
		if depot.Name() == depotName {
			return depot
		}
	}
	return nil
}

func (d *MasterDepot) LinkedContent(ctx context.Context, s Chunk) (remote.ContentLink, error) {
	depot := d.getDepot(s.Depot)
	if depot == nil {
		return remote.ContentLink{}, fmt.Errorf("cannot find depot %q", s.Depot)
	}
	return depot.LinkedContent(ctx, s)
}

func (d *MasterDepot) LinkedContents(ctx context.Context, contentDesc string) ([]remote.ContentLink, error) {
	var chunks []Chunk
	if err := json.Unmarshal([]byte(contentDesc), &chunks); err != nil {
		return nil, err
	}
	return d.LinkedContentsChunks(ctx, chunks)
}

func (d *MasterDepot) LinkedContentsChunks(ctx context.Context, chunks []Chunk) ([]remote.ContentLink, error) {
	links := make([]remote.ContentLink, len(chunks))
	for i, s := range chunks {
		depot := d.getDepot(s.Depot)
		if depot == nil {
			return nil, fmt.Errorf("cannot find depot %q", s.Depot)
		}
		link, err := depot.LinkedContent(ctx, s)
		if err != nil {
			return nil, err
		}
		links[i] = link
	}
	return links, nil
}
