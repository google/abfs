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

package abfsrepo

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPathLevels(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  [][]string
	}{
		{
			name:  "flat",
			paths: []string{"build/make", "art", "frameworks/base", "external/foo"},
			want:  [][]string{{"art", "build/make", "external/foo", "frameworks/base"}},
		},
		{
			name:  "parent listed first",
			paths: []string{"a", "a/child"},
			want:  [][]string{{"a"}, {"a/child"}},
		},
		{
			name:  "child listed first",
			paths: []string{"a/child", "a"},
			want:  [][]string{{"a"}, {"a/child"}},
		},
		{
			name:  "chain and siblings",
			paths: []string{"a/b/c", "a-b", "a/b", "a/bc", "a", "b/x", "b"},
			want:  [][]string{{"a", "a-b", "b"}, {"a/b", "a/bc", "b/x"}, {"a/b/c"}},
		},
		{
			name:  "nested below a path that is no project",
			paths: []string{"vendor/x/y", "vendor/x/y/z", "vendor/w"},
			want:  [][]string{{"vendor/w", "vendor/x/y"}, {"vendor/x/y/z"}},
		},
		{
			name:  "paths not clean",
			paths: []string{"a/", "./a/child", "b//c", "b"},
			want:  [][]string{{"a/", "b"}, {"./a/child", "b//c"}},
		},
		{
			name:  "none",
			paths: nil,
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, PathLevels(tt.paths))
		})
	}
}
