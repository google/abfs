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

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

func TestIgnoreMatch_GitPaths(t *testing.T) {
	tests := []struct {
		name   string
		chroot string
		path   string
		want   bool
	}{
		{name: "source file", path: "frameworks/base/Foo.java"},
		{name: "repo git state", path: ".repo/projects/build/soong.git/HEAD", want: true},
		{name: "project git dir", path: "build/soong/.git/HEAD", want: true},
		{name: "source file below a top", chroot: "android", path: "frameworks/base/Foo.java"},
		{name: "sibling of a top", chroot: "android", path: "../tools/flash.sh"},
		{name: "repo git state above a top", chroot: "android", path: "../.repo/projects/other/app.git/HEAD", want: true},
		{name: "project git dir below a top", chroot: "android", path: "build/soong/.git/HEAD", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &ABFSBackend{chroot: tt.chroot, config: &Configuration{}, matchFiles: newMatchFiles("out")}
			require.Equal(t, tt.want, c.ignoreMatch(&entities.PatchEntry{Path: tt.path}))
		})
	}
}
