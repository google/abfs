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

package importer

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
)

type importerCmd struct {
	*root.RootCmd
	workers  int
	force    bool
	preserve bool
}

func NewCmd(rootCmd *root.RootCmd) *importerCmd {
	return &importerCmd{RootCmd: rootCmd}
}

func (c *importerCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "importer",
		Usage:   "<source dir> <destination dir>",
		Aliases: []string{},
		Desc:    "Recursively copy non-cas source directory to casfs destination directory",
	}
}
func (c *importerCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.IntVarP(&c.workers, "jobs", "n", runtime.NumCPU()*2, "Number of concurrent workers")
	fl.BoolVarP(&c.force, "force", "f", false, "Force copy")
	fl.BoolVarP(&c.preserve, "preserve", "p", false, "Preserve timestamps and file metadata")
}

func (c *importerCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	src, dst := args.EnsureTwoArgs(fl)

	opts := CopyOptions{
		Src:        src,
		Dst:        dst,
		MaxWorkers: c.workers,
		Force:      c.force,
		Preserve:   c.preserve,
		Finalize: func(res FinalizeResult) error {
			_, err := bindcas.Hash(res.Path)
			return err
		},
	}
	start := time.Now()
	_, err := ParallelCopyTopDown(opts)
	if err != nil {
		return clierr.CLIErr("Failed: %v", err)
	}
	copyTime := time.Since(start)
	fmt.Printf("\nCalculating root hash...")
	hashStart := time.Now()
	h, err := bindcas.Hash(dst)
	fmt.Println("")
	if err != nil {
		return clierr.CLIErr("Failed to get final hash of %s: %v", dst, err)
	}
	hashTime := time.Since(hashStart)
	fmt.Println("Copy time ", copyTime)
	fmt.Println("Hash time ", hashTime)
	fmt.Println("")
	fmt.Printf("%s: %s\n", dst, h)
	return nil
}
