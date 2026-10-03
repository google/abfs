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

package tests

import (
	"os"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/cas/oms"
	casfs "github.com/google/abfs/cas/fs"
	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/cas/hash/hashers"

	"github.com/google/abfs/casfuse"
	"github.com/google/abfs/casfuse/fs"
	"github.com/google/abfs/casfuse/fs/fsapi"
	ctrlgit "github.com/google/abfs/casfuse/fs/fsapi/ctrl/git"
	"github.com/google/abfs/casfuse/fs/fscas"
	"github.com/google/abfs/casfuse/fs/fscas/stores"
	"github.com/google/abfs/casfuse/fs/fscas/tap"
	"github.com/google/abfs/casfuse/fs/mem"
)

type Env struct {
	tmpDir      string
	hasher      hash.Hasher
	store       cas.Store
	storeWriter fscas.StoreWriter
	gitStore    ctrlgit.Store
}

func AllTestSetup(rootDir string) (*Env, error) {
	tmpDir, err := os.MkdirTemp(rootDir, "")
	if err != nil {
		return nil, err
	}
	hasher := hashers.CreateHasher(hashers.HasherTypeSHA1)
	cacheDir, err := os.MkdirTemp(tmpDir, "")
	if err != nil {
		return nil, err
	}

	casStore := oms.CreateObjectModel(oms.ObjectModelTypeGIT, hasher, cacheDir, "", true)
	if err := casStore.Open(); err != nil {
		return nil, err
	}
	storeWriter, gitStore := stores.CreateStore(stores.StoreTypeGit, casStore)

	out := &Env{tmpDir: tmpDir,
		hasher:      hasher,
		store:       casStore,
		storeWriter: storeWriter,
		gitStore:    gitStore,
	}

	return out, nil
}
func AllTestTearDown(e *Env) {
	if e == nil {
		return
	}
	e.store.Close()
	os.RemoveAll(e.tmpDir)

}

func (e *Env) createRootDir() casfuse.Dir {
	ofs := fs.NewFS(0, 0, 0)
	tap := tap.NewTap(e.hasher)
	ofs.RegisterExt(casfuse.CASTap, tap)
	ofs.RegisterExt(casfuse.GitExt, e.gitStore)

	dir := fs.NewRootDir(ofs)
	nf := mem.NewNodeFactory(dir)
	dir.SetNodeFactory(nf)
	ofs.SetRoot(dir)
	return dir
}
func (e *Env) createRootCASDir() (casfuse.Dir, casfuse.API) {
	ofscas := fs.NewFS(1, 0, 0)
	// Hook cas to fs
	tap := tap.NewTap(e.hasher)
	fscas.RegisterCAS(ofscas, e.hasher, e.store, e.storeWriter, tap)

	fscas.NewRootDir(ofscas)

	return ofscas.Root().Inode().(casfuse.Dir), fsapi.NewCASAPI(ofscas)

}
func (e *Env) createAPI() casfs.FSAPI {
	rootDir := e.createRootDir()
	api := casfuse.NewFSAPI(rootDir.FS())
	return api

}
