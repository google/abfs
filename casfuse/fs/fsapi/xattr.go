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

package fsapi

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/abfs/cas/fs"
	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/casfuse"
)

// Handle all xattr interfaces here, in the future these could be seperated out
// with registered handlers
const (
	XattrDebug       = "debug"
	XattrDiags       = "diags"
	XattrType        = "type"
	XattrFsStatus    = "fs_status"
	XattrHash        = "hash"
	XattrHashSize    = "hash_size"
	XattrHashStat    = "hash_stat"
	XattrDirListHash = "dir_list_hash"
	XattrDeepTree    = "deeptree"
	XattrLoaded      = "loaded"
	XattrDirty       = "dirty"
	XattrExclude     = "exclude"
	XattrCtrl        = "ctrl"
)
const (
	ctrlTypeControlName = "ctrl"
	ctrlTypeMemName     = "mem"
	ctrlTypeGitName     = "git"
	ctrlTypeGitObjName  = "gitobj"
)

func xattrName(fullname string) (string, error) {
	prefix, name, found := strings.Cut(fullname, ".")
	if !found {
		return "", casfuse.ErrNotSupported
	}
	if prefix == "user" {
		name = strings.TrimPrefix(name, "casfs_")
	}
	return name, nil
}

func xattrBoolToStr(val bool) string {
	if val {
		return "1"
	} else {
		return "0"
	}
}
func xattrStrToBool(val string) (bool, error) {
	if val == "1" {
		return true, nil
	}
	if val == "0" {
		return false, nil
	}
	return false, casfuse.ErrInvalidXattrVal
}
func boolErr(val bool, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return xattrBoolToStr(val), nil
}

type Xattr struct {
	api    casfuse.API
	hasher hash.Hasher
}

func NewXattr(api casfuse.API, hasher hash.Hasher) *Xattr {
	return &Xattr{api: api, hasher: hasher}

}
func (x *Xattr) GetXattr(ctx context.Context, inode casfuse.Inode, fullname string) (string, error) {

	name, err := xattrName(fullname)
	if err != nil {
		return "", casfuse.ErrInvalidXattrVal
	}
	switch name {
	case XattrDebug:
		return boolErr(x.api.Debug(inode))
	case XattrDiags:
		// Add support or remove
		return "", nil
	case XattrType:
		return "", nil // TODO/deprecate this
	case XattrFsStatus:
		s, err := x.api.Status()
		if err != nil {
			return "", err
		}
		return boolErr(s.Ready, nil)
	case XattrHash:
		h, err := x.api.Hash(ctx, inode)
		if err != nil {
			return "", err
		}
		return h.String(), nil
	case XattrHashSize:
		h, sz, err := x.api.HashSize(ctx, inode)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s:%d", h, sz), nil
	case XattrHashStat:
		st, err := x.api.HashStat(ctx, inode)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s:%d:%d", st.Hash, st.Size, fs.FSModeToOSMode(st.Mode)), nil
	case XattrDirListHash:
		h, err := x.api.DirList(ctx, inode)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s", h), nil
	case XattrLoaded:
		return boolErr(x.api.Loaded(inode))
	case XattrDirty:
		return boolErr(x.api.Dirty(inode))
	case XattrExclude:
		return boolErr(x.api.Excluded(inode))
	case XattrCtrl:
	case XattrDeepTree:
	}

	return "", casfuse.ErrInvalidXattrVal
}
func (x *Xattr) SetXattr(ctx context.Context, inode casfuse.Inode, fullname, value string) error {
	name, err := xattrName(fullname)
	if err != nil {
		return casfuse.ErrInvalidXattrVal
	}
	switch name {
	case XattrDebug:
		if val, err := xattrStrToBool(value); err == nil {
			return x.api.SetDebug(inode, val)
		}

	case XattrHash:
		if hash, err := x.hasher.NewHash(value); err == nil {
			return x.api.SetHash(inode, hash)
		}
	case XattrHashSize:
		hashStr, sizeStr, found := strings.Cut(value, ":")
		if !found {
			return casfuse.ErrInvalidXattrVal
		}
		size, err := strconv.ParseInt(sizeStr, 10, 64)
		if err != nil {
			return casfuse.ErrInvalidXattrVal
		}
		hash, err := x.hasher.NewHash(hashStr)
		if err != nil {
			return casfuse.ErrInvalidXattrVal
		}
		return x.api.SetHashSize(inode, hash, size)

	case XattrExclude:
		val, err := xattrStrToBool(value)
		if err != nil {
			return err
		}
		return x.api.SetExcluded(inode, val)
	case XattrCtrl:
		ctrlStr, dirName, found := strings.Cut(value, ":")
		if !found {
			return casfuse.ErrInvalidXattrVal
		}
		switch ctrlStr {
		case ctrlTypeControlName:
			return x.api.CreateControl(ctx, inode, dirName)
		case ctrlTypeMemName:
			return x.api.CreateRamfs(ctx, inode, dirName)
		case ctrlTypeGitName:
			gitDir, shaStr, found := strings.Cut(dirName, ":")
			if !found {
				return casfuse.ErrInvalidXattrVal
			}
			return x.api.CreateGit(ctx, inode, gitDir, shaStr)
		case ctrlTypeGitObjName:
			return x.api.CreateGitObjects(ctx, inode, dirName)
		}

	case XattrDeepTree:
		hash, err := x.hasher.NewHash(value)
		if err != nil {
			return casfuse.ErrInvalidXattrVal
		}
		return x.api.SetDeepTree(inode, hash)

	case XattrDiags:
	case XattrType:
	case XattrFsStatus:
	case XattrHashStat:
	case XattrDirListHash:
	case XattrLoaded:
	case XattrDirty:

	}

	return casfuse.ErrInvalidXattrVal
}
