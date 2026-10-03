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
	"bytes"
	"context"
	"fmt"
	"path"
	"sort"
	"syscall"
	"time"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/hash"

	"github.com/google/abfs/cas/cas/oms/git"
	"github.com/google/abfs/casfuse/fs"
	"github.com/google/abfs/casfuse/fs/fscas"
	"github.com/google/abfs/casfuse/fs/mem"
)

var configStr string = fmt.Sprintf(`[core]
	compression = 0
	looseCompression = 0
	abbrev = no

[fetch]
	unpackLimit = 2147483647
[maintenance.loose-objects]
	auto = 0

[maintenance.incremental-repack]
	auto = 0
[casfs]
	noObjectCompress = true
	flatObjectPath = true
`)

func ProjectCreate(gitDir, projRoot fs.FsDir, headHash hash.Hash) error {
	// Create index
	index, err := createIndex(gitDir, projRoot)
	if err != nil {
		return err
	}
	// Create HEAD
	head := fs.NewFile(gitDir, cas.FileModeFile(), mem.NewBlobString(headHash.String()))
	// Create refs dir
	refs := fs.NewDir(gitDir, cas.FileModeDir(), mem.NewNodeFactory(gitDir))
	// Create objects
	objsDir := NewObjectsDir(gitDir)
	// Create configuration
	config := fs.NewFile(gitDir, cas.FileModeFile(), mem.NewBlobString(configStr))

	gitDir.AddChild("index", index)
	gitDir.AddChild("HEAD", head)
	gitDir.AddChild("refs", refs)
	gitDir.AddChild("objects", objsDir)
	gitDir.AddChild("config", config)

	return nil

}

func createIndex(gitDir, projRoot fs.FsDir) (fs.Node, error) {
	body := git.NewIndex(2)
	if err := createIndexProc(projRoot, "", body); err != nil {
		return nil, err
	}
	buf := new(bytes.Buffer)
	if err := body.Write(buf); err != nil {
		return nil, err
	}
	file := fs.NewFile(gitDir, cas.FileModeFile(), mem.NewBlobBytes(buf.Bytes()))
	// Add time to file
	file.SetMtime(time.Now().Add(time.Duration(time.Second)))
	return file, nil
}
func createIndexProc(dir fs.FsDir, fullPath string, body *git.Index) error {
	dir.Lock()
	defer dir.Unlock()
	entries := dir.InternalEntriesLocked()
	// sort keys
	keys := make([]string, 0, len(*entries))
	for k := range *entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	base := path.Base(fullPath)

	casDir, ok := dir.(fscas.Node)
	if !ok {
		return fmt.Errorf("Non-cas node found")
	}

	cacheTreeEntry := body.CreateCacheTreeEntry(base, casDir.Hash())
	startIndexEntries := body.NumEntries()
	numSubtrees := 0

	for _, k := range keys {
		n := (*entries)[k]

		if k == "." || k == ".." || k == ".git" {
			continue
		}
		var name string
		if fullPath != "" {
			name = fullPath + "/" + k
		} else {
			name = k
		}

		casNode, ok := n.(fscas.Node)
		if !ok {
			continue
		}

		attr := n.Attr(context.Background(), nil)
		mode := attr.Mode

		if n.Mode().IsDir() {
			numSubtrees++
			err := createIndexProc(n.(fs.FsDir), name, body)
			if err != nil {
				return err
			}
			continue
		} else if cas.FileModeIsSymLink(n.Mode()) {
			mode = syscall.S_IFLNK
		} else if n.Mode().IsRegular() {
			mode |= syscall.S_IFREG
		}
		body.AddEntry(name,
			git.IndexEntryData{
				CtimeSeconds: uint32(attr.Ctime.Unix()),
				CtimeMillis:  uint32(attr.Ctime.UnixMilli() % 1000),
				MtimeSeconds: uint32(attr.Mtime.Unix()),
				MtimeMillis:  uint32(attr.Mtime.UnixMilli() % 1000),
				Dev:          uint32(attr.Rdev),
				Ino:          uint32(attr.Ino),
				Mode:         uint32(mode),
				Uid:          uint32(attr.Uid),
				Gid:          uint32(attr.Gid),
				FileSize:     uint32(attr.Size),
				SHA1:         git.ConvHash(casNode.Hash()),
				Flags:        uint16(len(name)),
			})

	}
	endIndexEntries := body.NumEntries()
	body.UpdateTree(cacheTreeEntry, endIndexEntries-startIndexEntries, numSubtrees)
	return nil
}
