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

package fscas

import (
	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/casfuse"
	"github.com/google/abfs/casfuse/fs"
	"github.com/google/abfs/casfuse/fs/fscas/tap"
)

type casFS struct {
	cas       cas.Store
	casWriter StoreWriter
	tap       tap.Tap
}

func RegisterCAS(f *fs.FS, hasher hash.Hasher, cas cas.Store, casWriter StoreWriter, tap tap.Tap) {
	obj := &casFS{cas: cas, casWriter: casWriter, tap: tap}
	// Register the CAS interface for the filesystem
	f.SetHasher(hasher)
	f.RegisterExt(casfuse.CASExt, obj)
}
func GetStore(f casfuse.FS) cas.Store {
	return f.GetExt(casfuse.CASExt).(*casFS).cas
}
func GetStoreWriter(f casfuse.FS) StoreWriter {
	return f.GetExt(casfuse.CASExt).(*casFS).casWriter
}
func GetTap(f casfuse.FS) tap.Tap {
	return f.GetExt(casfuse.CASExt).(*casFS).tap
}
