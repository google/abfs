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

package dumpindex

import (
	"log"
	"os"
	"path/filepath"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/cas/cmd/args"
	"github.com/google/abfs/cas/cmd/fs/root"
	"github.com/google/abfs/cas/fs/gitindex"
)

type cmd struct {
	rootCmd *root.RootCommand
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{rootCmd: rootCmd}
}

func (c *cmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *cmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:  "dump-index",
		Usage: "<PATH_TO_INDEX_OR_GIT_DIR>",
		Desc: `Dumps the entries and cached TREE extension of a Git index file in a human-readable table.
`,
	}
}

func (c *cmd) Run(fl *pflag.FlagSet) {
	target := args.EnsureSingleArg(fl)

	// If a repository directory or .git dir is passed, point to the index file
	fi, err := os.Stat(target)
	if err != nil {
		log.Fatalf("failed to stat %s: %v", target, err)
	}

	indexPath := target
	if fi.IsDir() {
		if filepath.Base(target) == ".git" {
			indexPath = filepath.Join(target, "index")
		} else {
			indexPath = filepath.Join(target, ".git", "index")
		}
	}

	dump, err := gitindex.Dump(indexPath)
	if err != nil {
		log.Fatalf("failed to dump index %s: %v", indexPath, err)
	}

	dump.PrintDump(os.Stdout)
}
