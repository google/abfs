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

package casfuse

import (
	"errors"
	"io/fs"
	"syscall"
)

var (
	ErrEntryNotFound   = fs.ErrNotExist
	ErrNotADirectory   = errors.New("entry is not a directory")
	ErrNotAFile        = errors.New("entry is not a file")
	ErrEntryExists     = fs.ErrExist
	ErrDirNotEmpty     = syscall.ENOTEMPTY
	ErrNotSupported    = errors.New("not supported")
	ErrUnknownXattr    = errors.New("unknown xattr")
	ErrInvalidXattrVal = errors.New("invalid xattr value")
	ErrInvalidPath     = errors.New("invalid path")

	ErrTryAgain             = errors.New("try again")
	ErrInvalidState         = errors.New("invalid state")
	ErrDirty                = errors.New("dirty")
	ErrNotDirty             = errors.New("not dirty")
	ErrSnapshotStuck        = errors.New("snapshot stuck")
	ErrNotCASNode           = errors.New("not cas node")
	ErrInvalidSession       = errors.New("invalid session")
	ErrSessionAlreadyExists = errors.New("session already exists")
)
