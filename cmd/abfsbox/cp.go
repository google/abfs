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
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"syscall"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/shared/entities"
)

type CopyCmd struct {
	recursive bool
}

func NewCopyCmd() *CopyCmd {
	return &CopyCmd{}
}

func (c *CopyCmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:    "cp",
		Usage:   "[OPTION] SOURCE DEST\n    or: [OPTION] SOURCE... DIRECTORY",
		Aliases: []string{},
		Desc:    "Copy SOURCE to DEST, or multiple SOURCE(s) to DIRECTORY via xattr\n",
	}
}
func (c *CopyCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.recursive, "recursive", "r", false, "copy directories recursively")

}
func (c *CopyCmd) copySingle(src, dest string) error {

	stat, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("cannot stat source file %v", err)
	}
	// Check destination
	destStat, destStatErr := os.Lstat(dest)
	if destStatErr == nil {
		if destStat.IsDir() && !stat.IsDir() {
			dest = path.Join(dest, filepath.Base(src))
			destStat, destStatErr = os.Lstat(dest)
		}
	}
	if stat.Mode().IsDir() {
		if err := os.MkdirAll(dest, 0755); err != nil {
			return fmt.Errorf("failed to make directory for dest %w", err)
		}
		// Only create the directory here for recursive copy will do copy of the
		// files so we don't set the directory hash directly.
		return nil

	}

	srcHash, err := GetXAttr(src, HashSizeAttrName)
	if err != nil {
		return fmt.Errorf("failed to read file hash_size %w", err)
	}

	if entities.IsNotExist(destStatErr) {
		// Create file
		if err := os.MkdirAll(path.Dir(dest), 0755); err != nil {
			return err
		}
	} else {
		existHash, err := GetXAttr(dest, HashSizeAttrName)
		if err != nil {
			return fmt.Errorf("failed to get hash/size for %s", dest)
		}
		// File exists
		if existHash == srcHash {
			if stat.Mode() != destStat.Mode() {
				if err := os.Chmod(dest, stat.Mode()); err != nil {
					return err
				}
			}
			return nil

		} else {
			if err := os.Remove(dest); err != nil {
				return fmt.Errorf("failed to remove existing file with different hash")
			}
		}
	}

	if stat.Mode()&os.ModeSymlink == os.ModeSymlink {
		// Read the data to recreate the symlink
		if target, err := os.Readlink(src); err != nil {
			return fmt.Errorf("failed to read source link")
		} else if err := os.Symlink(target, dest); err != nil {
			return fmt.Errorf("failed to create symlink to %s: %w", target, err)
		}
	} else if stat.Mode().IsRegular() {
		hash, err := GetXAttr(src, HashSizeAttrName)
		if err != nil {
			return fmt.Errorf("failed to read file hash_size %w", err)
		}
		if err := syscall.Mknod(dest, syscall.S_IFREG|uint32(stat.Mode()), 0); err != nil {
			return fmt.Errorf("failed to create dest file: %w", err)
		}
		err = SetXAttr(dest, HashSizeAttrName, hash)
		if err != nil {
			return fmt.Errorf("failed to set file hash to %s %w", hash, err)
		}

	}
	return nil
}

func (c *CopyCmd) recursiveCopy(src, dest string) error {
	// src is a directory want to use relative path versus src
	failed := false
	basedir := path.Dir(src)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// Keep going if WalkDir couldn't walk into the given directory
			log.Printf("cp: %s couldn't walk", p)
			return nil
		}

		relPath, err := filepath.Rel(basedir, p)
		if err != nil {
			log.Printf("cp: cannot get rel path for %s  (%s)\n", p, basedir)
			failed = true
			return nil

		}
		newDest := path.Join(dest, relPath)

		destStat, err := os.Lstat(newDest)
		if err == nil {
			if destStat.IsDir() && !d.IsDir() {
				log.Printf("cp: cannot overwrite directory '%s' with non-directory '%s'\n",
					newDest, p)
				failed = true
				return nil
			}
		}
		err = c.copySingle(p, newDest)
		if err != nil {
			fmt.Println(err)
			failed = true
		}
		return nil

	})
	if failed {
		return fmt.Errorf("") // return error with no message
	}
	return err
}
func (c *CopyCmd) doCopy(src, dest string) error {
	stat, err := os.Lstat(src)

	if err != nil {
		return fmt.Errorf("cp: %v", err)
	}
	if stat.IsDir() {
		if c.recursive {
			return c.recursiveCopy(src, dest)
		} else {
			return fmt.Errorf("cp: -r not specified; omitting directory '%s'", src)
		}
	} else {
		if err := c.copySingle(src, dest); err != nil {
			return fmt.Errorf("cp %s %s: %v", src, dest, err)
		}
	}
	return nil
}
func (c *CopyCmd) Run(fl *pflag.FlagSet) {
	log.SetFlags(0)
	if fl.NArg() < 2 {
		fl.Usage()
		os.Exit(-1)
	}
	if fl.NArg() == 2 {
		src := fl.Arg(0)
		dest := fl.Arg(1)
		if err := c.doCopy(src, dest); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}
	// Last argument must be a valid directory
	dirIdx := fl.NArg() - 1
	dir := fl.Arg(dirIdx)

	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() {
			log.Fatalf("cp: target '%s': Not a directory", dir)
		}
	} else {
		log.Fatalf("cp: target '%s': No such file or directory", dir)

	}
	failed := false
	for i := range dirIdx {
		src := fl.Arg(i)

		if err := c.doCopy(src, dir); err != nil {
			log.Printf("%s\n", err)
			failed = true
		}
	}

	if failed {
		os.Exit(1)
	}
}
