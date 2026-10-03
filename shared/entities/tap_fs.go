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

package entities

import (
	"fmt"
	"io/fs"
)

type TapType int

const (
	TapFSMkdir       TapType = 0
	TapFSSymlink     TapType = 1
	TapFSLink        TapType = 2
	TapFSUnlink      TapType = 3
	TapFSRename      TapType = 4
	TapFSReadDir     TapType = 5
	TapFSSymlinkRead TapType = 6
	TapFSStat        TapType = 7
	TapFSOpenRead    TapType = 8
	TapFSOpenWrite   TapType = 9
	TapFSAttrChange  TapType = 10
)

func (t TapType) String() string {
	switch t {
	case TapFSMkdir:
		return "mkdir"
	case TapFSSymlink:
		return "symlink"
	case TapFSLink:
		return "link"
	case TapFSUnlink:
		return "unlink"
	case TapFSRename:
		return "rename"
	case TapFSReadDir:
		return "readdir"
	case TapFSSymlinkRead:
		return "symlink-read"
	case TapFSStat:
		return "stat"
	case TapFSOpenRead:
		return "open"
	case TapFSOpenWrite:
		return "create"
	case TapFSAttrChange:
		return "attr-change"
	default:
		return fmt.Sprintf("unknown tap type %d", t)
	}
}

// Use one generic type for all entries
type TapFSEntry struct {
	Type    TapType
	Dirty   bool
	IsARI   bool
	Path    string
	ARI     string
	Hash    Hash
	AuxPath string
	Mode    fs.FileMode
	Size    int64
}
