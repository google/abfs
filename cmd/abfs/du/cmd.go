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

package du

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/dustin/go-humanize"
	"github.com/spf13/pflag"

	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

type duCmd struct {
	*root.RootCmd
	*cmd.StoreProvider
	hash        entities.Hash
	human       bool
	blockSize   int64
	fileCount   int64
	dirCount    int64
	totalSize   int64
	totalBlocks int64
}

func NewCmd(rootCmd *root.RootCmd) *duCmd {
	return &duCmd{RootCmd: rootCmd}
}

func (c *duCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "du",
		Usage:   "<dir-hash>",
		Aliases: []string{},
		Desc:    `Summarize device usage of the set of FILEs, recursively for directories.`,
	}
}

func (c *duCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.human, "human-readable", "h", false, "Print sizes in human readable format (e.g., 1K 234M 2G)")
	fl.Int64VarP(&c.blockSize, "block-size", "b", 1024, "Size of a block to calculate the total blocks volume")
}

func (c *duCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	hexHash := args.EnsureSingleArg(fl)
	if _, err := hex.Decode(c.hash[:], []byte(hexHash)); err != nil {
		return err
	}

	if err := c.du(ctx); err != nil {
		return err
	}

	fmt.Printf("Files count: %s\n", c.humanCount(c.fileCount))
	fmt.Printf("Directories count: %s\n", c.humanCount(c.dirCount))
	fmt.Printf("Size sum: %s\n", c.humanBytes(c.totalSize))
	fmt.Printf("Size occupied: %s\n", c.humanBytes(c.totalBlocks*c.blockSize))
	return nil
}

func (c *duCmd) humanCount(cc int64) string {
	if c.human {
		return humanize.Comma(cc)
	}
	return fmt.Sprintf("%d", cc)
}

func (c *duCmd) humanBytes(cc int64) string {
	if c.human {
		return humanize.Bytes(uint64(cc))
	}
	return fmt.Sprintf("%d", cc)
}

func (c *duCmd) du(ctx context.Context) error {
	// Walk does not visit root dir
	if err := c.visit(".", entities.FileModeDir, c.hash); err != nil {
		return err
	}
	return casgit.Walk(ctx, c.hash, c.Store, c.visit)
}

func (c *duCmd) visit(path string, mode entities.FileMode, hash entities.Hash) error {
	if mode == entities.FileModeDir {
		c.dirCount++
	} else {
		c.fileCount++
	}

	switch mode {
	case entities.FileModeSymlink, entities.FileModeRegular, entities.FileModeExecutable:
		size, err := c.getFileSize(hash)
		if err != nil {
			return err
		}
		c.totalSize += size
		c.totalBlocks += (size + c.blockSize - 1) / c.blockSize
	case entities.FileModeDir:
		// a guess for a dir size
		c.totalBlocks += 1
	}

	return nil
}

func (c *duCmd) getFileSize(hash entities.Hash) (s int64, retErr error) {
	f, err := c.Store.OpenBlob(context.Background(), hash)
	if err != nil {
		return 0, err
	}
	defer entities.CloseErrCheck(f, &retErr)

	if ff, isFile := f.(*os.File); isFile {
		stat, err := ff.Stat()
		if err != nil {
			return 0, err
		}
		return stat.Size(), nil
	}
	return 0, nil
}
