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

package hashpath

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/google/abfs/shared/entities"
)

func TestParse(t *testing.T) {
	h := "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name    string
		input   string
		want    *HashPath
		wantErr bool
		err     string
	}{
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
			err:     "expected a hash-path but got empty string",
		},
		{
			name:  "only hash",
			input: h,
			want: &HashPath{
				Hash: entities.NewHash(h),
			},
		},
		{
			name:    "invalid hash",
			input:   "invalid-hash",
			wantErr: true,
			err:     `expected a hash when path is not used: "invalid-hash"`,
		},
		{
			name:  "hash with path",
			input: h + ":path/to/file",
			want: &HashPath{
				Hash: entities.NewHash(h),
				Path: "path/to/file",
			},
		},
		{
			name:  "hash with dot path",
			input: h + ":.",
			want: &HashPath{
				Hash: entities.NewHash(h),
				Path: ".",
			},
		},
		{
			name:  "hash with empty path",
			input: h + ":",
			want: &HashPath{
				Hash: entities.NewHash(h),
				Path: ".",
			},
		},
		{
			name:  "simple ref, empty path",
			input: "ref:",
			want: &HashPath{
				Ref:  "ref",
				Path: ".",
			},
		},
		{
			name:  "ref with path",
			input: "ref:path/to/file",
			want: &HashPath{
				Ref:  "ref",
				Path: "path/to/file",
			},
		},
		{
			name:  "ref and project with path",
			input: "ref@project:path/to/file",
			want: &HashPath{
				Ref:     "ref",
				Project: "project",
				Path:    "path/to/file",
			},
		},
		{
			name:  "ref, project, and server with path",
			input: "ref@project@server:path/to/file",
			want: &HashPath{
				Ref:     "ref",
				Project: "project",
				Server:  "server",
				Path:    "path/to/file",
			},
		},
		{
			name:    "empty revision",
			input:   "@project:path",
			wantErr: true,
			err:     `empty revision is not supported in hash-path: "@project:path"`,
		},
		{
			name:    "path with leading slashes",
			input:   h + ":///path/to/file",
			wantErr: true,
			err:     `cannot accept absolute path ("///path/to/file") in a hash-path`,
		},
		{
			name:    "path with parent dir",
			input:   h + ":../path/to/file",
			wantErr: true,
			err:     `cannot accept path ("../path/to/file") with parent directory in a hash-path`,
		},
		{
			name:    "path that is just ..",
			input:   h + ":..",
			wantErr: true,
			err:     `cannot accept path ("..") with parent directory in a hash-path`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				assert.EqualError(t, err, tt.err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestHashPathString(t *testing.T) {
	h := "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "only hash",
			input: h,
			want:  h,
		},
		{
			name:  "hash with path",
			input: h + ":path/to/file",
			want:  h + ":path/to/file",
		},
		{
			name:  "hash with dot path",
			input: h + ":.",
			want:  h + ":.",
		},
		{
			name:  "hash with empty path",
			input: h + ":",
			want:  h + ":.",
		},
		{
			name:  "simple ref, empty path",
			input: "ref:",
			want:  "ref:.",
		},
		{
			name:  "ref with path",
			input: "ref:path/to/file",
			want:  "ref:path/to/file",
		},
		{
			name:  "ref and project with path",
			input: "ref@project:path/to/file",
			want:  "ref@project:path/to/file",
		},
		{
			name:  "ref, project, and server with path",
			input: "ref@project@server:path/to/file",
			want:  "ref@project@server:path/to/file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hp, err := Parse(tt.input)
			if err != nil {
				t.Errorf("could not parse hash-path %q", tt.input)
			}
			assert.Equal(t, tt.want, hp.String())
		})
	}
}
