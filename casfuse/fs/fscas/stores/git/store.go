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
	"context"
	"fmt"
	"io"
	"os"

	"github.com/google/abfs/cas/cas"
	omsgit "github.com/google/abfs/cas/cas/oms/git"
	"github.com/google/abfs/cas/hash"

	ctrlgit "github.com/google/abfs/casfuse/fs/fsapi/ctrl/git"
	"github.com/google/abfs/casfuse/fs/fscas"
)

type Store struct {
	gs       *omsgit.CASStore
	provider cas.Provider
}

var _ fscas.StoreWriter = (*Store)(nil)
var _ ctrlgit.Store = (*Store)(nil)

func NewStore(s cas.Store) *Store {
	if r, ok := s.(*cas.Remote); ok {
		store, provider := r.GetBase()
		return &Store{gs: store.(*omsgit.CASStore),
			provider: provider}
	}
	return &Store{gs: s.(*omsgit.CASStore)}
}

func (s *Store) FilePath(h hash.Hash) (string, os.FileInfo, error) {
	path, info, err := s.gs.Blobs.Path(h)
	if err == nil {
		return path, info, err
	}
	if s.provider == nil {
		return path, info, err
	}
	if err := s.provider.FetchFile(h); err != nil {
		return "", nil, err
	}
	return s.gs.Blobs.Path(h)
}
func (s *Store) CreateStagingFileInum(inum int) (string, int, error) {
	return s.gs.Blobs.CreateScratchFileInum(inum)
}
func (s *Store) CreateStagingFile() (string, int, error) {
	return s.gs.Blobs.CreateScratchFile()
}
func (s *Store) RemoveStagingFile(f string) error {
	return s.gs.Blobs.ScratchRemove(f)
}
func (s *Store) Hasher() hash.Hasher {
	return s.gs.Blobs.Hasher
}
func (s *Store) GetPath(h hash.Hash) (*ctrlgit.Info, error) {
	/* Order:
	   - local object
	   - local blob
	   - remote object
	   - remote blob
	*/
	filepath, fi, err := s.gs.Objects.Path(h)
	if err == nil {
		out := &ctrlgit.Info{GitType: ctrlgit.GitTypeObject, FilePath: filepath, FileInfo: fi}
		return out, nil
	}
	filepath, fi, err = s.gs.Blobs.Path(h)
	if err == nil {
		out := &ctrlgit.Info{GitType: ctrlgit.GitTypeBlob, FilePath: filepath, FileInfo: fi}
		return out, nil
	}

	if err := s.provider.FetchFile(h); err != nil {
		return nil, err
	}

	filepath, fi, err = s.gs.Objects.Path(h)
	if err == nil {
		out := &ctrlgit.Info{GitType: ctrlgit.GitTypeObject, FilePath: filepath, FileInfo: fi}
		return out, nil
	}
	filepath, fi, err = s.gs.Blobs.Path(h)
	if err == nil {
		out := &ctrlgit.Info{GitType: ctrlgit.GitTypeBlob, FilePath: filepath, FileInfo: fi}
		return out, nil
	}

	return nil, fmt.Errorf("notfound")
}
func (s *Store) WriteBlob(ctx context.Context, hash hash.Hash, reader io.Reader) error {
	return s.gs.Blobs.WriteBlob(hash, reader)
}
func (s *Store) WriteObject(ctx context.Context, hash hash.Hash, reader io.Reader) error {
	return s.gs.Objects.WriteObject(hash, reader)
}
