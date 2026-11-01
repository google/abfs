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

package ws

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
)

func TestMountRepoCmd_RepoInitArgs(t *testing.T) {
	const manURL = "https://gerrit.example.com/platform/manifest"
	tests := []struct {
		name string
		opts MountRepoOpts
		want []string
	}{
		{
			name: "branch file",
			opts: MountRepoOpts{MountPoint: "/mnt/w1", Branch: "main", BranchFile: "product.xml"},
			want: []string{"repo", "init", manURL, "-b", "main", "-m", "product.xml"},
		},
		{
			name: "default branch file",
			opts: MountRepoOpts{MountPoint: "/mnt/w1", Branch: "main", BranchFile: "default.xml"},
			want: []string{"repo", "init", manURL, "-b", "main", "-m", "default.xml"},
		},
		{
			name: "empty branch file",
			opts: MountRepoOpts{MountPoint: "/mnt/w1", Branch: "main"},
			want: []string{"repo", "init", manURL, "-b", "main"},
		},
		{
			name: "pinned file wins",
			opts: MountRepoOpts{MountPoint: "/mnt/w1", Branch: "main", ManifestFile: "/src/pinned.xml", BranchFile: "product.xml"},
			want: []string{"repo", "init", manURL, "-b", "main", "-m", "/mnt/w1/.repo/pinned.xml"},
		},
		{
			name: "pinned hash wins",
			opts: MountRepoOpts{MountPoint: "/mnt/w1", ManifestHash: entities.NewHash("0123456789abcdef0123456789abcdef01234567"), BranchFile: "product.xml"},
			want: []string{"repo", "init", manURL, "-m", "/mnt/w1/.repo/pinned.xml"},
		},
		{
			name: "groups and fallback",
			opts: MountRepoOpts{MountPoint: "/mnt/w1", Branch: "main", BranchFile: "product.xml", Groups: []string{"a", "b"}, FallbackToGit: true},
			want: []string{"repo", "init", manURL, "-g", "a,b", "-b", "main", "-m", "product.xml", "--fallback-to-git"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewMountRepoCmd(root.NewRootCommand(), tt.opts)
			c.ConfigProvider = &cmd.ConfigProvider{Config: &instance.Config{
				ManifestScheme:      "https",
				ManifestServer:      "gerrit.example.com",
				ManifestProjectName: "platform/manifest",
			}}
			require.Equal(t, tt.want, c.repoInitArgs())
		})
	}
}

func TestMountRepoCmd_BranchFileFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "default", args: []string{"--tmp-ws", "-b", "main", "/mnt/w1"}, want: "default.xml"},
		{name: "set", args: []string{"--tmp-ws", "-b", "main", "--branch-file", "product.xml", "/mnt/w1"}, want: "product.xml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newMountRepoCmd(&wsCmd{RootCmd: root.NewRootCommand()})
			fl := pflag.NewFlagSet("mount-repo", pflag.ContinueOnError)
			c.RegisterFlags(fl)
			require.NoError(t, fl.Parse(tt.args))
			require.Equal(t, tt.want, c.BranchFile)
		})
	}
}
