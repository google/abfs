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

package ls

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/pflag"

	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/hashpath"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/store"
)

type lsCmd struct {
	*root.RootCmd
	*cmd.StoreProvider
	out io.Writer

	noColor     bool
	noDir       bool
	longMode    bool
	noRecursive bool
}

func NewCmd(rootCmd *root.RootCmd) *lsCmd {
	return &lsCmd{RootCmd: rootCmd, out: os.Stdout}
}

func (c *lsCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "ls",
		Usage:   "[-f] [--no-color] <dir-hash-path>|<dir>",
		Aliases: []string{},
		Desc:    `Show list of item in a directory`,
	}
}

func (c *lsCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVar(&c.noColor, "no-color", false, "Do not use color")
	fl.BoolVarP(&c.noDir, "no-dir", "f", false, "Do not include directories")
	fl.BoolVarP(&c.longMode, "long", "l", false, "Enable long mode")
	fl.BoolVarP(&c.noRecursive, "no-recursive", "R", false, "Disable the recursive mode")
}

func (c *lsCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	o, _ := os.Stdout.Stat()
	if (o.Mode() & os.ModeCharDevice) != os.ModeCharDevice {
		c.noColor = true
	}

	arg := args.EnsureSingleArg(fl)
	if _, err := os.Stat(arg); err == nil {
		if err := c.listDir(arg); err != nil {
			return err
		}
		return nil
	}
	hp, err := hashpath.Parse(arg)
	if err != nil {
		return fmt.Errorf("invalid hash-path: %v", err)
	}

	res, err := hashpath.Resolve(ctx, c.Store, hp, entities.ABFSMetaServer, entities.ABFSMetaProject)
	if err != nil {
		return err
	}
	hash := res.Resolved

	return c.list(ctx, hash)
}

func (c *lsCmd) listDir(dir string) error {
	err := filepath.Walk(dir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if c.noRecursive && info.IsDir() && path != dir {
			return filepath.SkipDir
		}
		var loaded = "X"
		l, err := bindcas.Loaded(path)
		if err == nil {
			if l {
				loaded = "1"
			} else {
				loaded = "0"
			}
		}
		// TODO: this will cause a snapshot
		hash, err := bindcas.Hash(path)
		if err != nil {
			hash = entities.ZeroHash
		}
		color := ""
		if !c.noColor {
			switch loaded {
			case "X":
				color = Purple
			case "1":
				color = c.osModeToColor(info.Mode())
			default:
				color = Gray
			}
		}
		f, _ := filepath.Rel(dir, path)
		_, _ = fmt.Fprintf(c.out, "%s %s %s  %s%s%s\n",
			loaded, info.Mode(), hash, color, f, c.modeToColor(0))

		return nil

	})
	return err
}

func (c *lsCmd) list(ctx context.Context, hash entities.Hash) error {
	hdr, err := c.Store.ObjectHeader(ctx, hash)
	if err != nil {
		return err
	}
	if hdr.Type != gitobjects.ObjectTypeDir {
		return fmt.Errorf("expected %s to be a tree (directory) but is %s", hash, hdr.Type)
	}

	return casgit.Walk(ctx, hash, c.Store, c.visit)
}

var (
	Reset  = "\033[0m"
	Red    = "\033[1;31m"
	Green  = "\033[1;32m"
	Yellow = "\033[1;33m"
	Blue   = "\033[1;34m"
	Purple = "\033[1;35m"
	Cyan   = "\033[1;36m"
	Gray   = "\033[1;37m"
	White  = "\033[1;97m"
)

func (c *lsCmd) visit(path string, mode entities.FileMode, hash entities.Hash) error {
	if !c.noDir || mode != entities.FileModeDir {
		if c.longMode {
			if err := c.printLong(path, mode, hash); err != nil {
				return err
			}
		} else {
			if err := c.printShort(path, mode, hash); err != nil {
				return err
			}
		}
	}

	if c.noRecursive {
		return casgit.ErrWalkNoStepIn
	}
	return nil
}

func (c *lsCmd) printShort(path string, mode entities.FileMode, hash entities.Hash) error {
	_, err := fmt.Fprintf(c.out, "%s %s %s%s%s\n", entities.ModeToString(mode), hash, c.modeToColor(mode), path, c.modeToColor(0))
	return err
}

func (c *lsCmd) printLong(path string, mode entities.FileMode, hash entities.Hash) error {
	if mode == entities.FileModeSymlink {
		target, err := c.getSymlinkTarget(hash)
		if err != nil {
			return err
		}

		_, err = fmt.Fprintf(c.out, "%s %s %10d %s%s%s -> %s\n", entities.ModeToString(mode), hash, len(target), c.modeToColor(mode), path, c.modeToColor(0), target)
		if err != nil {
			return err
		}
	} else {
		var size int64
		if mode == entities.FileModeRegular || mode == entities.FileModeExecutable {
			var err error
			size, err = c.getFileSize(hash)
			if err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(c.out, "%s %s %10d %s%s%s\n", entities.ModeToString(mode), hash, size, c.modeToColor(mode), path, c.modeToColor(0))
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *lsCmd) getSymlinkTarget(hash entities.Hash) (string, error) {
	content, err := store.ReadAllBlob(context.Background(), c.Store, hash)
	if err != nil {
		return "", err
	}
	return string(content), err
}

func (c *lsCmd) getFileSize(hash entities.Hash) (size int64, retErr error) {
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

func (c *lsCmd) osModeToColor(m fs.FileMode) string {
	if c.noColor {
		return ""
	}

	if m.IsDir() {
		return Blue
	}
	if m&fs.ModeSymlink == fs.ModeSymlink {
		return Cyan
	}

	if m.IsRegular() {
		if m&0111 != 0 {
			return Green
		}
		return ""
	}
	return Red

}
func (c *lsCmd) modeToColor(m entities.FileMode) string {
	if c.noColor {
		return ""
	}

	switch m {
	case 0:
		return Reset
	case 040000:
		return Blue
	case 0100644:
		return "" // default color
	case 0100755:
		return Green
	case 0120000:
		return Cyan
	default:
		return Red
	}
}
