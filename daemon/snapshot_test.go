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

package daemon

import (
	"context"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
)

// A saved workspace is restored in the order repo sync checks it out: a git repo whose work dir
// lies inside another repo's work dir comes after that repo.
func TestGitRepoLevels(t *testing.T) {
	tests := []struct {
		name     string
		workDirs []string
		want     [][]string
	}{
		{name: "flat", workDirs: []string{"b", "a"}, want: [][]string{{"a", "b"}}},
		{
			name:     "nested",
			workDirs: []string{"a/child/deep", "a/child", "b", "a-b", "a", "vendor/x/y"},
			want:     [][]string{{"a", "a-b", "b", "vendor/x/y"}, {"a/child"}, {"a/child/deep"}},
		},
		{name: "none", workDirs: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var repos []binding.GitRepoSnapshot
			for _, d := range tt.workDirs {
				repos = append(repos, binding.GitRepoSnapshot{WorkDir: d, GitDir: d + ".git"})
			}
			var got [][]string
			for _, level := range gitRepoLevels(repos) {
				var dirs []string
				for _, p := range level {
					require.Equal(t, p.WorkDir+".git", p.GitDir)
					dirs = append(dirs, p.WorkDir)
				}
				got = append(got, dirs)
			}
			require.Equal(t, tt.want, got)
		})
	}
}

// A repo is restored only after every repo whose work dir holds its work dir.
func TestRestoreInLevels(t *testing.T) {
	workDirs := []string{"a/child/deep", "a/child", "b", "a"}
	var repos []binding.GitRepoSnapshot
	for _, d := range workDirs {
		repos = append(repos, binding.GitRepoSnapshot{WorkDir: d})
	}
	var lock sync.Mutex
	done := make(map[string]bool)
	err := restoreInLevels(context.Background(), repos, func(ctx context.Context, p binding.GitRepoSnapshot) error {
		lock.Lock()
		for d := path.Dir(p.WorkDir); d != "."; d = path.Dir(d) {
			if !done[d] {
				t.Errorf("%s restored before %s", p.WorkDir, d)
			}
		}
		lock.Unlock()
		// Give a repo restored too early the time to start.
		time.Sleep(10 * time.Millisecond)
		lock.Lock()
		done[p.WorkDir] = true
		lock.Unlock()
		return nil
	})
	require.NoError(t, err)
	require.Len(t, done, len(workDirs))
}
