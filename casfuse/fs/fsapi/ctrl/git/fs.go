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
	"io"
	"os"

	"github.com/google/abfs/cas/hash"

	"github.com/google/abfs/casfuse"
)

type GitType int

const (
	GitTypeObject GitType = iota
	GitTypeBlob
	GitTypeTree
	GitTypeCommit
)

type Info struct {
	GitType  GitType
	FilePath string
	FileInfo os.FileInfo
}
type Store interface {
	Hasher() hash.Hasher
	GetPath(hash.Hash) (*Info, error)
	WriteBlob(ctx context.Context, hash hash.Hash, reader io.Reader) error
	WriteObject(ctx context.Context, hash hash.Hash, reader io.Reader) error
}

func NewGit(f casfuse.FS, s Store) {
	f.RegisterExt(casfuse.GitExt, s)
}
func GetGit(f casfuse.FS) Store {
	return f.GetExt(casfuse.GitExt).(Store)
}
