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

package fuses

import (
	"github.com/google/abfs/casfuse"
	"github.com/google/abfs/casfuse/fs/fsapi"
)

type FuseType int

const ()

func (o FuseType) String() string {
	return ""
}

// Fuse interface
type Fuse interface {
	// Setup filesystem mount at the given point
	Mount(mntPoint, fsName, subTypeName string) error
	// Unmount filesystem
	Unmount() error
	// Serve the filesystem after it has been mounted
	Serve(fs casfuse.FS, xattr *fsapi.Xattr) error
	// Close the server IPC
	Close() error
	// Set the runtime debug
	SetDebug(bool)
}

// CreateFuse create fuse based on type
func CreateFuse(t FuseType) Fuse {
	return nil
}
