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

package cas

import (
	"path/filepath"
	"runtime/debug"
	"testing"

	"github.com/google/abfs/cas/hash"
	"github.com/stretchr/testify/require"
)

// Create [target]
//
//	[symlink]
func TestResolve_SymLink(t *testing.T) {
	ts := newTestStore()
	targetFileName := "target"
	symlinkFileName := "symlink"

	slinkHash, err := ts.SaveSymLink(targetFileName)
	require.NoError(t, err)
	taEntry := DirEntry{Hash: hashFile(targetFileName, 300), Size: 300, Mode: FileModeFile(), Name: targetFileName}
	slEntry := DirEntry{Hash: slinkHash, Size: int64(len(targetFileName)), Mode: FileModeSymLink(), Name: symlinkFileName}

	h, err := ts.SaveDirEntries(NewDirInMemory([]DirEntry{taEntry, slEntry}))

	de, err := Resolve(ts, h, symlinkFileName)
	require.NoError(t, err)
	require.Equal(t, taEntry, *de)

}

// Create
//
//		[dir][dir][dir][dir][target]
//	     [symlink]
func TestResolve_SymlinkDeep(t *testing.T) {
	ts := newTestStore()
	var createDir func(level int) hash.Hash
	targetFileName := "target"

	const maxLevel = 10
	var path string
	for _ = range maxLevel {
		path = filepath.Join(path, "dir")
	}
	path = filepath.Join(path, targetFileName)

	symlinkFileName := "symlink"

	slinkHash, err := ts.SaveSymLink(path)

	taEntry := DirEntry{Hash: hashFile(targetFileName, 300), Size: 300, Mode: FileModeFile(), Name: targetFileName}
	slEntry := DirEntry{Hash: slinkHash, Size: int64(len(path)), Mode: FileModeSymLink(), Name: symlinkFileName}

	createDir = func(level int) hash.Hash {
		if level == 0 {
			h, err := ts.SaveDirEntries(NewDirInMemory([]DirEntry{taEntry}))
			require.NoError(t, err)
			return h
		}
		des := []DirEntry{}
		if level == maxLevel {
			// Add symlink as well directory
			des = append(des, slEntry)
		}
		level--
		ch := createDir(level)
		des = append(des, DirEntry{Hash: ch, Size: 0, Mode: FileModeDir(), Name: "dir"})
		h, err := ts.SaveDirEntries(NewDirInMemory(des))
		require.NoError(t, err)
		return h
	}
	h := createDir(maxLevel)
	de, err := Resolve(ts, h, symlinkFileName)
	require.NoError(t, err)
	require.Equal(t, taEntry, *de)
}

func TestResolvePanic(t *testing.T) {

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{
			name:    "Trailing slash or final directory",
			path:    "config/git-pusher",
			wantErr: false,
		},
		{
			name:    "Single segment directory",
			path:    "folder",
			wantErr: false,
		},
	}
	ts := newTestStore()
	var err error
	var configDirHash hash.Hash
	var folderDirHash hash.Hash
	var rootDirHash hash.Hash
	// Create config directory
	{
		des := []DirEntry{}
		pusher := DirEntry{Hash: hashFile("git-pusher", 300), Size: 300, Mode: FileModeFile(), Name: "git-pusher"}
		des = append(des, pusher)
		configDirHash, err = ts.SaveDirEntries(NewDirInMemory(des))
		require.NoError(t, err)
	}
	// Create folder
	{
		des := []DirEntry{}
		folderDirHash, err = ts.SaveDirEntries(NewDirInMemory(des))
		require.NoError(t, err)

	}
	// Create top level directory
	{
		des := []DirEntry{}
		des = append(des, DirEntry{Hash: configDirHash, Size: 0, Mode: FileModeDir(), Name: "config"})
		des = append(des, DirEntry{Hash: folderDirHash, Size: 0, Mode: FileModeDir(), Name: "folder"})
		rootDirHash, err = ts.SaveDirEntries(NewDirInMemory(des))
		require.NoError(t, err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This defer/recover block catches the panic so the test can report it
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Resolve() panicked with: %v\nStack: %s", r, debug.Stack())
				}
			}()

			// Mock objects (replace with your actual mock implementation)
			_, err := Resolve(ts, rootDirHash, tt.path)

			if (err != nil) != tt.wantErr {
				t.Errorf("Resolve() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	// Define our test cases
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{
			name:    "Symlink with Trailing Slash",
			path:    "link/file",
			wantErr: false,
		},
		{
			name:    "Symlink to Dot Path",
			path:    "shortcut/file",
			wantErr: false,
		},
		{
			name:    "Invalid Path Segment",
			path:    "config/nonexistent",
			wantErr: true,
		},
	}

	/*
			|── config/                <-- Directory
		        |   └── config-dir/        <-- Directory
			|      └── file            <-- file
			└── link                   <-- Symlink -> "config/config-dir/"
			└── shortcut               <-- Symlink -> "./config/../config/config-dir"
	*/

	ts := newTestStore()
	var err error
	var configDirHash hash.Hash
	var rootDirHash hash.Hash
	// Create config directory
	{
		var cDirHash hash.Hash
		{
			des := []DirEntry{}
			file := DirEntry{Hash: hashFile("file", 300), Size: 300, Mode: FileModeFile(), Name: "file"}
			des = append(des, file)
			cDirHash, err = ts.SaveDirEntries(NewDirInMemory(des))
			require.NoError(t, err)
		}
		des := []DirEntry{}
		des = append(des, DirEntry{Hash: cDirHash, Size: 0, Mode: FileModeDir(), Name: "config-dir"})
		configDirHash, err = ts.SaveDirEntries(NewDirInMemory(des))
		require.NoError(t, err)
	}
	// Create root
	{
		linkTarget := "config/config-dir"
		shortcutTarget := "./config/../config/config-dir"
		linkHash, err := ts.SaveSymLink(linkTarget)
		require.NoError(t, err)
		shortcutHash, err := ts.SaveSymLink(shortcutTarget)
		require.NoError(t, err)
		slink := DirEntry{Hash: linkHash, Size: int64(len(linkTarget)), Mode: FileModeSymLink(), Name: "link"}
		shortcut := DirEntry{Hash: shortcutHash, Size: int64(len(shortcutTarget)), Mode: FileModeSymLink(), Name: "shortcut"}

		des := []DirEntry{}
		des = append(des, DirEntry{Hash: configDirHash, Size: 0, Mode: FileModeDir(), Name: "config"})
		des = append(des, slink)
		des = append(des, shortcut)
		rootDirHash, err = ts.SaveDirEntries(NewDirInMemory(des))
		require.NoError(t, err)

	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Catch panics to identify exactly which case fails
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Panic in test %q: %v", tt.name, r)
				}
			}()
			got, err := Resolve(ts, rootDirHash, tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("Resolve() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err == nil && got == nil {
				t.Error("Resolve() returned nil entry and nil error")
			}
		})
	}
}
