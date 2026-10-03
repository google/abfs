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

package cacheman

import (
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const channelSize = 100000000

type validateCmd struct {
	*cachemanCmd
	cacheDir string
	jobs     int
	errors   int
	lock     sync.Mutex
}

func NewValidateCmd(c *cachemanCmd) *validateCmd {
	return &validateCmd{cachemanCmd: c}
}

func (c *validateCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "validate",
		Usage: "",
		Desc:  `Validate all the objects in the cache.`,
	}
}

func (c *validateCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.IntVarP(&c.jobs, "jobs", "j", runtime.NumCPU(), "Concurrent files to examine")
}

func (c *validateCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	c.cacheDir = c.Config.CacheDir

	c.validateObjects(path.Join(c.cacheDir, "blobs"), "Validating blobs", c.validateBlobFile)
	fmt.Println()
	c.validateObjects(path.Join(c.cacheDir, "objects"), "Validating objects", c.validateObjectFile)
	fmt.Println()

	if c.errors != 0 {
		return clierr.CLIErr("Found %d errors!\n", c.errors)
	}

	fmt.Println("All good!")
	return nil
}

func (c *validateCmd) reportError(bar *progressbar.ProgressBar, file string, err error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	_, _ = progressbar.Bprintf(bar, "error in %s: %s\n", file, err)

	c.errors++
}

func (c *validateCmd) validateBlobFile(blobPath string) error {
	segs := strings.Split(blobPath, string(os.PathSeparator))
	if len(segs) < 2 {
		return fmt.Errorf("invalid blob path")
	}
	fileHash := segs[len(segs)-2] + segs[len(segs)-1]
	if !entities.IsHash(fileHash) {
		return nil // Can be a temp file
	}

	hash, _, err := objects.HashFileBlob(blobPath)
	if err != nil {
		return err
	}

	if fileHash != hash.String() {
		return fmt.Errorf("invalid hash expected %s, cached file hash is %s", fileHash, hash)
	}
	return nil
}

func (c *validateCmd) validateObjectFile(objPath string) (retErr error) {
	if strings.HasSuffix(objPath, ".tree") {
		return nil
	}
	segs := strings.Split(objPath, string(os.PathSeparator))
	if len(segs) < 2 {
		return fmt.Errorf("invalid object path")
	}
	fileHash := segs[len(segs)-2] + segs[len(segs)-1]
	if !entities.IsHash(fileHash) {
		return nil // Can be a temp file
	}

	f, err := os.Open(objPath)
	if err != nil {
		return err
	}
	defer func() { retErr = entities.FirstError(retErr, f.Close()) }()

	zr, err := zlib.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { retErr = entities.FirstError(retErr, zr.Close()) }()

	hasher := objects.NewHasher()
	_, err = io.Copy(hasher, zr)
	if err != nil {
		return err
	}

	var calcedHash entities.Hash
	hasher.Sum(calcedHash[:0])

	if fileHash != calcedHash.String() {
		return fmt.Errorf("invalid hash expected %s, cached file hash is %s", fileHash, calcedHash)
	}
	return nil
}

func (c *validateCmd) validateObjects(objDir string, desc string, validate func(string) error) {
	bar := progressbar.NewOptions64(1,
		progressbar.OptionSetDescription(fmt.Sprintf("%-20s", desc)),
		progressbar.OptionSetRenderBlankState(true),
		progressbar.OptionShowCount(),
	)
	defer func() {
		if err := bar.Finish(); err != nil {
			log.Printf("error finishing bar: %s", err)
		}
	}()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	stopTicker := make(chan struct{})
	defer close(stopTicker)

	var done, total atomic.Int64
	go func() {
		for {
			select {
			case <-stopTicker:
				return
			case <-ticker.C:
				if bar.GetMax64() != total.Load() {
					bar.ChangeMax64(total.Load())
				}
				if bar.State().CurrentNum != done.Load() {
					if err := bar.Set64(done.Load()); err != nil {
						log.Printf("error setting bar: %s", err)
					}
				}
			}
		}
	}()

	var wg sync.WaitGroup

	toCheck := make(chan string, channelSize)
	for range c.jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for file := range toCheck {
				if err := validate(file); err != nil {
					c.reportError(bar, file, err)
				}
				done.Add(1)
			}
		}()
	}

	for i := range 0x1000 {
		dir := path.Join(objDir, fmt.Sprintf("%0.3x", i))
		entries, err := os.ReadDir(dir)
		if err != nil {
			c.reportError(bar, dir, err)
			continue
		}
		total.Add(int64(len(entries)))
		for _, ent := range entries {
			file := path.Join(dir, ent.Name())

			toCheck <- file
		}
	}
	close(toCheck)
	wg.Wait()
}
