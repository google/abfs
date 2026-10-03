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

package ares

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/entities"
)

type createPatchCmd struct {
	*ARESCmd
	*cmd.StoreProvider
}

func NewCreatePatchCmd(c *ARESCmd) *createPatchCmd {
	return &createPatchCmd{ARESCmd: c}
}

func (c *createPatchCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "create-patch",
		Usage:   "<path/to/file1> <path/to/file2:new/path/to/file2>...",
		Aliases: []string{},
		Desc:    `Helper to create a patch based on files in an ABFS mount`,
	}
}

func (c *createPatchCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *createPatchCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return c.run(ctx, fl.Args())
}

func (c *createPatchCmd) run(ctx context.Context, args []string) error {
	patch := make(entities.Patch, 0, len(args))
	for _, a := range args {
		parts := strings.Split(a, ":")
		curPath := parts[0]
		patchPath := curPath
		if len(parts) == 2 {
			patchPath = parts[1]
		} else if len(parts) > 2 {
			return fmt.Errorf("invalid argument %q. Expected <filepath> or <filepath:newpath>", a)
		}
		hs, err := bindcas.HashStat(curPath)
		if err != nil {
			return fmt.Errorf("error with stat for file %q: %w", curPath, err)
		}
		patch = append(patch, &entities.PatchEntry{
			Path: patchPath,
			Mode: hs.Mode,
			Hash: hs.Hash,
			Size: hs.Size})
	}

	storePatch := &binding.PatchObject{
		Version: binding.CurrentPatchVersion,
		Patch:   patch,
	}

	if err := storePatch.Encode(); err != nil {
		return fmt.Errorf("error encoding patch: %w", err)
	}

	if err := c.Store.PutObject(ctx, storePatch.Hash, storePatch.Encoded); err != nil {
		return fmt.Errorf("error storing patch with hash %q: %w", storePatch.Hash, err)
	}

	fmt.Printf("successfully stored patch with hash %s\n", storePatch.Hash)
	return nil
}
