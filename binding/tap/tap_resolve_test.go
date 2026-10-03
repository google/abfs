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

package tap

import (
	"io/fs"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want []string
	}{
		{
			name: "Empty path",
			path: "",
			want: nil,
		},
		{
			name: "Root path",
			path: "/",
			want: nil,
		},
		{
			name: "Current directory",
			path: ".",
			want: nil,
		},
		{
			name: "Simple relative path",
			path: "a/b/c",
			want: []string{"a", "b", "c"},
		},
		{
			name: "Simple absolute path",
			path: "/a/b/c",
			want: []string{"a", "b", "c"},
		},
		{
			name: "Relative path with trailing slash",
			path: "a/b/",
			want: []string{"a", "b"},
		},
		{
			name: "Absolute path with trailing slash",
			path: "/a/b/",
			want: []string{"a", "b"},
		},
		{
			name: "Path with extra slashes",
			path: "a//b/c",
			want: []string{"a", "b", "c"},
		},
		{
			name: "Path with dot component",
			path: "a/./b",
			want: []string{"a", "b"},
		},
		{
			name: "Path with dot dot component",
			path: "a/b/../c",
			want: []string{"a", "c"},
		},
		{
			name: "Complex path",
			path: "/a/b/../../c/d/",
			want: []string{"c", "d"},
		},
		{
			name: "Single component path",
			path: "a",
			want: []string{"a"},
		},
		{
			name: "Single component absolute path",
			path: "/a",
			want: []string{"a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitPath(tt.path)
			// Handle special case for empty slices for comparison consistency
			if len(got) == 0 && len(tt.want) == 0 {
				return // They are considered equal
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

type fileOp struct {
	path string
	mode fs.FileMode
}

func TestTapTrack(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		tr := NewTapTrack()
		want := []fileOp{}
		verifyWalk(t, tr, want)
	})
	t.Run("add/remove", func(t *testing.T) {
		tr := NewTapTrack()
		tr.Add("/a/b", 0644)
		tr.Add("/a/c", 0644)
		tr.Remove("/a/b")

		want := []fileOp{
			{"/a/c", 0644},
		}
		verifyWalk(t, tr, want)
	})

	t.Run("add/rename", func(t *testing.T) {
		tr := NewTapTrack()
		tr.Add("/a/b", 0644)
		tr.Rename("/a/b", "/a/c", 0644)

		want := []fileOp{
			{"/a/c", 0644},
		}
		verifyWalk(t, tr, want)
	})

	t.Run("rename-dir", func(t *testing.T) {
		tr := NewTapTrack()
		tr.Add("/a/b/c", 0644)
		tr.Rename("/a/b", "/a/d", defDirMode)
		tr.Add("/a/d/e", 0644)

		want := []fileOp{
			{"/a/d/c", 0644},
			{"/a/d/e", 0644},
		}
		verifyWalk(t, tr, want)
	})

	t.Run("rename-dir-deep", func(t *testing.T) {
		tr := NewTapTrack()
		tr.Add("/a/b/c/d/e", 0644)
		tr.Rename("/a/b", "/f/g", defDirMode)
		tr.Add("/f/g/h", 0644)

		want := []fileOp{
			{"/f/g/c/d/e", 0644},
			{"/f/g/h", 0644},
		}
		verifyWalk(t, tr, want)
	})
}

func verifyWalk(t *testing.T, tr TapTrack, want []fileOp) {
	t.Helper()
	got := make([]fileOp, 0, len(want))
	err := tr.Walk(func(path string, mode fs.FileMode) error {
		got = append(got, fileOp{path, mode})
		return nil
	})
	require.NoError(t, err)

	sort.Slice(want, func(i, j int) bool { return want[i].path < want[j].path })
	sort.Slice(got, func(i, j int) bool { return got[i].path < got[j].path })

	if !reflect.DeepEqual(want, got) {
		t.Errorf("Walk() mismatch: want %v, got %v", want, got)
	}
}
