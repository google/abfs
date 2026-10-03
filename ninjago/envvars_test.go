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

package ninjago

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeEnvVars(t *testing.T) {
	tests := []struct {
		name       string
		cwd        string
		env        []string
		ignoreList map[string]struct{}
		expected   map[string]string
	}{
		{
			name: "replace cwd with CWD and strip prefix",
			cwd:  "/home/user/project",
			env: []string{
				"DIST_DIR=/home/user/project/out/dist",
				"OTHER_DIR=/other/path",
			},
			expected: map[string]string{
				"DIST_DIR":  "out/dist",
				"OTHER_DIR": "/other/path",
			},
		},
		{
			name: "relative path remains unchanged",
			cwd:  "/home/user/project",
			env: []string{
				"DIST_DIR=out/dist",
			},
			expected: map[string]string{
				"DIST_DIR": "out/dist",
			},
		},
		{
			name: "ignore list",
			cwd:  "/home/user/project",
			env: []string{
				"HOME=/home/user",
				"USER=user",
			},
			ignoreList: map[string]struct{}{
				"HOME": {},
				"USER": {},
			},
			expected: map[string]string{},
		},
		{
			name: "lists of paths should not be trimmed",
			cwd:  "/home/user/project",
			env: []string{
				"PATH=/home/user/project/bin:/usr/bin:/home/user/project/out/.path:rel/path",
			},
			expected: map[string]string{
				"PATH": "CWD/bin:/usr/bin:CWD/out/.path:rel/path",
			},
		},
		{
			name: "CWD at start but not followed by slash",
			cwd:  "/home/user/project",
			env: []string{
				"SOME_VAR=/home/user/project_suffix",
			},
			expected: map[string]string{
				"SOME_VAR": "CWD_suffix",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeEnvVars(tt.env, tt.cwd, func(key string) bool {
				_, ignore := tt.ignoreList[key]
				return !ignore
			})
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestDenormalizeEnvVars(t *testing.T) {
	tests := []struct {
		name     string
		cwd      string
		env      map[string]string
		expected []string
	}{
		{
			name: "replace CWD with cwd",
			cwd:  "/home/user/project",
			env: map[string]string{
				"DIST_DIR":  "CWD/out/dist",
				"OTHER_DIR": "CWD/other/path",
			},
			expected: []string{
				"DIST_DIR=/home/user/project/out/dist",
				"OTHER_DIR=/home/user/project/other/path",
			},
		},
		{
			name: "lists of paths",
			cwd:  "/home/user/project",
			env: map[string]string{
				"PATH": "CWD/bin:/usr/bin:CWD/out/.path:rel/path",
			},
			expected: []string{
				"PATH=/home/user/project/bin:/usr/bin:/home/user/project/out/.path:rel/path",
			},
		},
		{
			name: "CWD at start but not followed by slash",
			cwd:  "/home/user/project",
			env: map[string]string{
				"SOME_VAR": "CWD_suffix",
			},
			expected: []string{
				"SOME_VAR=/home/user/project_suffix",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DenormalizeEnvVars(tt.env, tt.cwd)
			assert.ElementsMatch(t, tt.expected, got)
		})
	}
}
