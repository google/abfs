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

package mount

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/cmd/abfs/ws"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

func TestMountCmd_MountRepoOpts(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	c := NewCmd(root.NewRootCommand())
	fl := pflag.NewFlagSet("mount", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	require.NoError(t, fl.Parse([]string{
		"--ws", "w1", "--tmp-ws", "-b", "main", "--branch-file", "product.xml",
		"-f", "/src/pinned.xml", "-h", hash, "-g", "a,b", "--force", "-r", "user/x",
		"-s", "-t", "good", "--manifest-server-username", "u", "--manifest-server-password", "p",
		"--fallback-to-git", "/mnt/w1",
	}))
	c.mountPoint = fl.Arg(0)
	c.isSnapshot = true

	want := ws.MountRepoOpts{
		MountPoint:             "/mnt/w1",
		TempWS:                 true,
		WSName:                 "w1",
		Branch:                 "main",
		ManifestHash:           entities.NewHash(hash),
		ManifestFile:           "/src/pinned.xml",
		BranchFile:             "product.xml",
		Groups:                 []string{"a", "b"},
		Force:                  true,
		RefPrefix:              "user/x",
		SmartSync:              true,
		SmartTag:               "good",
		ManifestServerUsername: "u",
		ManifestServerPassword: "p",
		IsSnapshot:             true,
		FallbackToGit:          true,
	}
	require.Equal(t, want, c.mountRepoOpts())
}
