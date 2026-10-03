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

package fs

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"syscall"
	"time"
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
		return "open-read"
	case TapFSOpenWrite:
		return "open-write"
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

type TapData struct {
	TapEntries []*TapFSEntry
}
type TapInfo struct {
	Pid          int
	ExitCode     int
	Rusage       *syscall.Rusage
	Timestamp    time.Time
	RunDuration  float64
	HashDuration float64
	Stdout       string
	Stderr       string
}
type TapCmdResult struct {
	TapData
	TapInfo
}

func (t *TapCmdResult) UnmarshalJSONWithHasher(data []byte, hasher Hasher) error {
	type tapFSEntry struct {
		Type    TapType
		Dirty   bool
		IsARI   bool
		Path    string
		ARI     string
		Hash    string
		AuxPath string
		Mode    fs.FileMode
		Size    int64
	}
	type tapCmdResult struct {
		TapEntries []*tapFSEntry
		TapInfo
	}
	var tmp tapCmdResult
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	entries := make([]*TapFSEntry, len(tmp.TapEntries))

	for i, r := range tmp.TapEntries {
		var h Hash
		var err error
		if r.IsARI {
			h = hasher.ZeroHash()
		} else {
			h, err = hasher.NewHash(r.Hash)
			if err != nil {
				return err
			}
		}
		entries[i] = &TapFSEntry{}
		entries[i].Type = r.Type
		entries[i].Dirty = r.Dirty
		entries[i].IsARI = r.IsARI
		entries[i].Path = r.Path
		entries[i].ARI = r.ARI
		entries[i].Hash = h
		entries[i].AuxPath = r.AuxPath
		entries[i].Mode = r.Mode
		entries[i].Size = r.Size
	}
	t.TapEntries = entries
	t.TapInfo = tmp.TapInfo
	return nil
}
