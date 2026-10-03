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

package main

import (
	"log"

	"go.coder.com/cli"

	"github.com/google/abfs/cas/cmd/fs/arisize"
	"github.com/google/abfs/cas/cmd/fs/control"
	"github.com/google/abfs/cas/cmd/fs/create"
	"github.com/google/abfs/cas/cmd/fs/debug"
	"github.com/google/abfs/cas/cmd/fs/deeptree"
	"github.com/google/abfs/cas/cmd/fs/dirlist"
	"github.com/google/abfs/cas/cmd/fs/dirty"
	"github.com/google/abfs/cas/cmd/fs/dumpindex"
	"github.com/google/abfs/cas/cmd/fs/excluded"
	"github.com/google/abfs/cas/cmd/fs/find"
	"github.com/google/abfs/cas/cmd/fs/git"
	"github.com/google/abfs/cas/cmd/fs/gitobj"
	"github.com/google/abfs/cas/cmd/fs/hash"
	"github.com/google/abfs/cas/cmd/fs/hashsize"
	"github.com/google/abfs/cas/cmd/fs/loaded"
	"github.com/google/abfs/cas/cmd/fs/ls"
	"github.com/google/abfs/cas/cmd/fs/root"
	"github.com/google/abfs/cas/cmd/fs/stat"
	"github.com/google/abfs/cas/cmd/fs/status"
	"github.com/google/abfs/cas/cmd/fs/tap"
)

func main() {
	log.SetFlags(0)
	r := root.NewRootCommand()
	r.Commands = []cli.Command{
		arisize.NewCmd(r),
		control.NewCmd(r),
		create.NewCmd(r),
		debug.NewCmd(r),
		deeptree.NewCmd(r),
		dirlist.NewCmd(r),
		dirty.NewCmd(r),
		dumpindex.NewCmd(r),
		excluded.NewCmd(r),
		find.NewCmd(r),
		hash.NewCmd(r),
		hashsize.NewCmd(r),
		loaded.NewCmd(r),
		ls.NewCmd(r),
		tap.NewCmd(r),
		git.NewCmd(r),
		gitobj.NewCmd(r),
		stat.NewCmd(r),
		status.NewCmd(r),
	}

	cli.RunRoot(r)
}
