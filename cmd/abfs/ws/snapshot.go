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
	"context"
	"fmt"
	"log"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/entities"
)

type snapshotCmd struct {
	*wsCmd
	restoreHex string
	abfs       *binding.Client
}

func newSnapshotCmd(c *wsCmd) *snapshotCmd {
	return &snapshotCmd{wsCmd: c}
}

func (c *snapshotCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "snapshot",
		Usage:   "[--restore <snapshot-hash>] <path>",
		Aliases: []string{"snap"},
		Desc:    `Create snapshot or restore one to a given working tree.`,
	}
}

func (c *snapshotCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.restoreHex, "restore", "r", "", "Restore a snapshot to a given working tree.")
}

func (c *snapshotCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	c.initWorkspaces()

	root := args.EnsureSingleArg(fl)

	abfs, err := binding.NewClient(root, entities.ABFSSocketFileName, c.Config.CacheDir, c.Config.LocalOnly, c.Config.FetchTimeout)
	if err != nil {
		return err
	}
	defer func() {
		if err := abfs.Close(); err != nil {
			log.Printf("Error closing abfs client: %v", err)
		}
	}()
	c.abfs = abfs

	if c.restoreHex == "" {
		snap, err := c.snapshot(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("%s\n", snap.Hash.String())
		return nil
	}
	if !entities.IsHash(c.restoreHex) {
		return clierr.CLIErr("restore hash is not valid")
	}
	snapHash := entities.NewHash(c.restoreHex)
	snap, err := binding.FetchObject[binding.WSSnapshot](ctx, c.abfs.Store(), snapHash)
	if err != nil {
		return err
	}

	if err := abfs.RestoreSnapshot(ctx, snap.Snapshot); err != nil {
		return err
	}
	fmt.Println("Done.")
	return nil
}

func (c *snapshotCmd) snapshot(ctx context.Context) (*binding.WSSnapshot, error) {
	snap, err := c.abfs.Snapshot(ctx)
	if err != nil {
		return nil, err
	}

	md, err := c.abfs.MountDetails()
	if err != nil {
		return nil, err
	}

	wsSnap := &binding.WSSnapshot{
		Version:      binding.CurrentWSSnapshotVersion,
		MountDetails: md.Hash,
		Time:         time.Now(),
		Snapshot:     snap,
	}
	if err := wsSnap.Encode(); err != nil {
		return nil, err
	}
	if err := c.abfs.Store().PutObject(ctx, wsSnap.Hash, wsSnap.Encoded); err != nil {
		return nil, err
	}
	return wsSnap, nil
}
