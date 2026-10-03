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
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/iafan/cwalk"
	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cacheman/protocol"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/store/localcache"
)

type cleanCmd struct {
	*cachemanCmd
	*cmd.CachemanProvider
	workers      int
	cleanBlobs   bool
	cleanMap     bool
	cleanObjects bool
	cleanTmp     bool
	cleanLogs    bool
	cleanIndex   bool
	all          bool
	force        bool
	background   bool
	hashes       []string

	toRemove chan string
}

func NewCleanCmd(c *cachemanCmd) *cleanCmd {
	return &cleanCmd{cachemanCmd: c}
}

func (c *cleanCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "clean",
		Usage: "[-j N] [-a] [-bg] [-b] [-f] [-m] [-o] [-t] [--hash SHA]...",
		Desc:  `Cleans on-disk cache for ABFS.`,
	}
}

func (c *cleanCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.IntVarP(&c.workers, "jobs", "n", runtime.NumCPU(), "Number of concurrent removers")
	fl.BoolVarP(&c.cleanBlobs, "blobs", "b", false, "Clean the blobs directory")
	fl.BoolVarP(&c.cleanMap, "map", "m", false, "Clean the map directory")
	fl.BoolVarP(&c.cleanObjects, "objects", "o", false, "Clean the objects directory")
	fl.BoolVarP(&c.cleanTmp, "tmp", "t", false, "Clean the tmp directory")
	fl.BoolVarP(&c.cleanLogs, "logs", "l", false, "Clean the logs directory")
	fl.BoolVarP(&c.all, "all", "a", false, "Clean entire cache")
	fl.BoolVarP(&c.force, "force", "f", false, "Force without confirmation")
	fl.BoolVar(&c.background, "bg", false, "Run in the background")
	fl.StringSliceVar(&c.hashes, "hash", nil, "Clean a given hash")
}

func (c *cleanCmd) cleanDir(ctx context.Context, directory string) {
	// get confirmation of the delete unless the --force option was used
	if !c.force {
		reader := bufio.NewReader(os.Stdin)
		log.Printf("Confirm delete of %s (Y/n)", directory)
		answer, _ := reader.ReadString('\n')
		answer = strings.ToUpper(answer)
		if answer != "\n" && strings.ReplaceAll(answer, "\n", "") != "Y" {
			log.Printf("Not confirmed with Y, clean skipped")
			return
		}
	}

	// check if the directory exists
	if _, err := os.Stat(directory); err != nil {
		if entities.IsNotExist(err) {
			log.Printf("%s does not exist", directory)
		} else {
			log.Printf("Error while cleaning %s: %v", directory, err)
		}
		return
	}

	err := cwalk.Walk(directory, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			c.remove(filepath.Join(directory, path))
		}
		return nil
	})
	if err != nil {
		log.Printf("failed to walk %s: %s", directory, err)
	}
}

func (c *cleanCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	if c.all {
		return c.cleanAll(ctx)
	}

	// Blobs, Objects, and individual hashes need to be handled by cacheman
	// to keep index consistent.
	cleanReq := protocol.CleanReq{
		Blobs:   c.cleanBlobs,
		Objects: c.cleanObjects,
		Hashes:  c.hashes,
	}
	if err := c.sendCleanReq(ctx, cleanReq); err != nil {
		return err
	}

	// Handle other types locally.
	c.cleanBlobs = false
	c.cleanObjects = false
	c.hashes = nil
	return c.run(ctx)
}

func cachemanActive() bool {
	if err := exec.Command("abfs", "cacheman", "status").Run(); err != nil {
		return false
	}

	return true
}

func stopCacheman() error {
	return exec.Command("abfs", "cacheman", "stop").Run()
}

func startCacheman() error {
	return exec.Command("abfs", "cacheman", "start").Run()
}

func (c *cleanCmd) sendCleanReq(ctx context.Context, req protocol.CleanReq) error {
	if err := c.Cacheman.Serve(ctx); err != nil {
		return err
	}

	return c.Cacheman.Clean(req)
}

func (c *cleanCmd) remove(p string) {
	c.toRemove <- p
}

// When everything is set to be cleaned, we can blow away everything
// outside of cacheman because the empty cache is a consistent index state.
// However, we need to stop cacheman to clear in-memory state.
func (c *cleanCmd) cleanAll(ctx context.Context) error {
	config := c.Config
	active := cachemanActive()

	if active {
		if err := stopCacheman(); err != nil {
			return fmt.Errorf("could not stop cacheman: %v", err)
		}
	}

	defer func() {
		if !active {
			return
		}

		if err := startCacheman(); err != nil {
			fmt.Printf("could not start cacheman: %v", err)
		}
	}()

	// Lock localcache to prevent cacheman from restarting during a clean.
	cache, err := localcache.NewCache(config.CacheDir, localcache.ExclusiveLockOpt())
	if err != nil {
		return err
	}
	defer func() {
		if err := cache.Unlock(); err != nil {
			log.Printf("failed to unlock cache: %s", err)
		}
	}()

	c.cleanBlobs = true
	c.cleanMap = true
	c.cleanObjects = true
	c.cleanTmp = true
	c.cleanLogs = true
	c.cleanIndex = true

	return c.run(ctx)
}

func (c *cleanCmd) run(ctx context.Context) error {
	config := c.Config
	var wg sync.WaitGroup
	wg.Add(c.workers)

	c.toRemove = make(chan string)

	for i := 0; i < c.workers; i++ {
		go func() {
			defer wg.Done()
			for p := range c.toRemove {
				if err := os.Remove(p); err != nil {
					log.Printf("failed to remove %s: %s", p, err)
				}
			}
		}()
	}

	cacheDir := config.CacheDir

	for _, h := range c.hashes {
		if !entities.IsHash(h) {
			log.Printf("ignoring %q because it is not a hash", h)
			continue
		}
		postfix := h[:3] + "/" + h[3:]

		c.remove(filepath.Join(cacheDir, "map", postfix))
		c.remove(filepath.Join(cacheDir, "blobs", postfix))
		c.remove(filepath.Join(cacheDir, "objects", postfix))
	}

	if c.cleanBlobs {
		c.cleanDir(ctx, filepath.Join(cacheDir, "blobs"))
	}

	if c.cleanMap {
		c.cleanDir(ctx, filepath.Join(cacheDir, "map"))
	}

	if c.cleanObjects {
		c.cleanDir(ctx, filepath.Join(cacheDir, "objects"))
	}

	if c.cleanLogs {
		// Note that while the cacheman daemon is running the space will not be released.
		fmt.Println("WARNING: The cachemand log is being removed, but systemd (if cacheman is running as a systemd service), will still write to that log. It is recommended to restart the cacheman service to ensure writes to stdout are not lost.")
		_ = os.Remove(filepath.Join(cacheDir, "../logs/cacheman"))
		_ = os.Remove(filepath.Join(cacheDir, "../logs/cachemand"))
	}

	if c.cleanTmp {
		c.cleanDir(ctx, filepath.Join(cacheDir, "tmp"))
	}

	if c.cleanIndex {
		c.cleanDir(ctx, filepath.Join(cacheDir, "index"))
	}

	close(c.toRemove)
	wg.Wait()
	return nil
}
