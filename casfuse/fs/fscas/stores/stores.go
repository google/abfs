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

package stores

import (
	"github.com/google/abfs/cas/cas"
	ctrlgit "github.com/google/abfs/casfuse/fs/fsapi/ctrl/git"
	"github.com/google/abfs/casfuse/fs/fscas"
	"github.com/google/abfs/casfuse/fs/fscas/stores/git"
)

type StoreType int

const (
	StoreTypeGit StoreType = iota
)

func (o StoreType) String() string {
	switch o {
	case StoreTypeGit:
		return "git"
	}
	return ""
}

// CreateStore create store based on type
func CreateStore(t StoreType, store cas.Store) (fscas.StoreWriter, ctrlgit.Store) {
	switch t {
	case StoreTypeGit:
		out := git.NewStore(store)
		return out, out
	}
	return nil, nil
}
