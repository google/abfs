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

package tar

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/spf13/pflag"

	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/store/localcache"
)

const (
	tarTypeHash = "hash"
	tarTypeFile = "file"
)

type tarCmd struct {
	*root.RootCmd
	store   entities.CASStore
	tw      *tar.Writer
	hashMap map[entities.Hash]bool
	tarType string
	tarFile string
	chdir   string
	gzip    bool
	hashes  []entities.Hash
}

func NewCmd(rootCmd *root.RootCmd) *tarCmd {
	return &tarCmd{
		RootCmd: rootCmd,
		hashMap: make(map[entities.Hash]bool),
	}
}

func (c *tarCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "tar",
		Usage:   "[-C <dir>] [-z] [-f <file>] [-t hash|file] [<hash|dir>...]",
		Aliases: []string{},
		Desc:    `tar a hash with all its accessible objects.`,
	}
}

func (c *tarCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.tarFile, "file", "f", "", "output tar filename (default stdout)")
	fl.BoolVarP(&c.gzip, "gz", "z", false, "compress the output with gzip")
	fl.StringVarP(&c.tarType, "type", "t", "hash", "type of tar [hash|file]")
	fl.StringVarP(&c.chdir, "directory", "C", "", "change to dir before performing any operations")
}

func getInputHash(input string) (entities.Hash, error) {
	if entities.IsHash(input) {
		return entities.NewHash(input), nil
	}
	_, err := os.Lstat(input)
	if err != nil {
		if entities.IsNotExist(err) {
			return entities.Hash{}, fmt.Errorf("the input %q is not a hash nor an existing CASFS directory", input)
		}
		return entities.Hash{}, fmt.Errorf("could not stat %s: %w", input, err)
	}
	hs, err := bindcas.HashStat(input)
	if err != nil {
		return entities.Hash{}, err
	}
	return hs.Hash, err
}

func (c *tarCmd) Exec(ctx context.Context, fl *pflag.FlagSet) (retErr error) {
	log.SetOutput(os.Stderr)

	if c.tarType != tarTypeHash && c.tarType != tarTypeFile {
		return clierr.CLIErr("Unknown tar type: %s", c.tarType)
	}

	if c.chdir != "" {
		if err := os.Chdir(c.chdir); err != nil {
			return fmt.Errorf("cannot change directory to %s: %v", c.chdir, err)
		}
	}

	c.hashes = make([]entities.Hash, len(fl.Args()))
	for i, input := range fl.Args() {
		hash, err := getInputHash(input)
		if err != nil {
			return err
		}
		c.hashes[i] = hash
	}

	// set up the output file
	var file *os.File
	var err error
	if c.tarFile == "" {
		file = os.Stdout
	} else {
		file, err = os.Create(c.tarFile)
		if err != nil {
			return err
		}
	}
	defer func() {
		retErr = entities.FirstError(retErr, file.Close())
		// This defer is going to execute last so check for error and log it if found
		if retErr == nil {
			log.Printf("Finished creating tar file %s", c.tarFile)
		}
	}()

	var writer io.Writer = file
	if c.gzip {
		zWriter := gzip.NewWriter(file)
		defer func() { retErr = entities.FirstError(retErr, zWriter.Close()) }()
		writer = zWriter
	}
	c.tw = tar.NewWriter(writer)
	defer func() { retErr = entities.FirstError(retErr, c.tw.Close()) }()

	return c.populateTar(ctx)
}

func (c *tarCmd) populateTar(ctx context.Context) error {
	for _, hash := range c.hashes {
		if c.tarType == tarTypeHash {
			// if adding the hashes, then add the initial directory file itself to the tar
			if err := c.addHashToTar("", entities.FileModeDir, hash); err != nil {
				return fmt.Errorf("failed to root %s: %w", hash, err)
			}
			if err := casgit.Walk(ctx, hash, c.store, c.addHashToTar); err != nil {
				return err
			}
		} else {
			if err := casgit.Walk(ctx, hash, c.store, c.addFileToTar); err != nil {
				return err
			}
		}
	}
	return nil
}

type Stater interface {
	Stat() (os.FileInfo, error)
}

func (c *tarCmd) openObject(ctx context.Context, hash entities.Hash, mode entities.FileMode) (reader io.ReadCloser, fi os.FileInfo, err error) {
	if mode == entities.FileModeDir {
		reader, err = c.store.OpenObjectFile(ctx, hash)
	} else {
		reader, err = c.store.OpenBlob(ctx, hash)
	}
	if err != nil {
		return
	}
	defer func() {
		if err != nil {
			_ = reader.Close()
		}
	}()

	stater, ok := reader.(Stater)
	if !ok {
		return nil, nil, fmt.Errorf("expected the reader object %s to support Stat() (type=%T)", hash, reader)
	}
	fi, err = stater.Stat()
	if err != nil {
		return nil, nil, err
	}
	return reader, fi, err
}

func (c *tarCmd) addHashToTar(path string, mode entities.FileMode, hash entities.Hash) (retErr error) {
	ctx := context.TODO()
	// skip if the hash was already added
	if _, exists := c.hashMap[hash]; exists {
		return nil
	}

	c.hashMap[hash] = true

	reader, fileInfo, err := c.openObject(ctx, hash, mode)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(reader, &retErr)

	topDirectory := localcache.BlobPrefix
	if mode == entities.FileModeDir {
		topDirectory = localcache.ObjectPrefix
	}

	relativePath := fmt.Sprintf("%s%s/%s", topDirectory, hash.String()[:3], hash.String()[3:])

	// get most of the file header info from the original file in the cache,
	// including timestamp but modify some properties for the tar file
	hdr, err := tar.FileInfoHeader(fileInfo, relativePath)
	if err != nil {
		return err
	}
	hdr.Name = relativePath
	hdr.Mode = 0600
	hdr.Size = fileInfo.Size()

	if err := c.tw.WriteHeader(hdr); err != nil {
		return err
	}

	if _, err := io.CopyN(c.tw, reader, fileInfo.Size()); err != nil {
		return err
	}

	return nil
}

func (c *tarCmd) addFileToTar(path string, mode entities.FileMode, hash entities.Hash) (retErr error) {
	ctx := context.TODO()

	reader, fileInfo, err := c.openObject(ctx, hash, mode)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(reader, &retErr)

	// get the contents of the file
	var contents bytes.Buffer
	contentsio := bufio.NewWriter(&contents)
	if _, err := io.Copy(contentsio, reader); err != nil {
		return err
	}

	tarMode, err := mode.ToOSFileMode()
	if err != nil {
		return err
	}

	// get most of the file header info from the original file in the cache,
	// including timestamp but modify some properties for the tar file
	hdr, err := tar.FileInfoHeader(fileInfo, path)
	if err != nil {
		return err
	}
	hdr.Name = path
	hdr.Mode = int64(tarMode)

	switch mode {
	case entities.FileModeDir:
		hdr.Typeflag = tar.TypeDir
	case entities.FileModeSymlink:
		hdr.Typeflag = tar.TypeSymlink
		hdr.Linkname = contents.String()
	default:
		hdr.Typeflag = tar.TypeReg
	}

	if err := c.tw.WriteHeader(hdr); err != nil {
		return err
	}

	if mode == entities.FileModeDir || mode == entities.FileModeSymlink {
		return nil
	}

	if _, err := c.tw.Write(contents.Bytes()); err != nil {
		return err
	}

	return nil
}
