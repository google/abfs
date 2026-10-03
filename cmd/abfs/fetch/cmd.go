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

package fetch

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

type fetchCmd struct {
	*root.RootCmd
	*cmd.StoreProvider
	fetchCommits bool
	fetchTrees   bool
	fetchBlobs   bool
	jobs         int

	todo chan entities.Hash
	from string

	commitCnt atomic.Uint64
	treeCnt   atomic.Uint64
	blobCnt   atomic.Uint64
}

func NewCmd(rootCmd *root.RootCmd) *fetchCmd {
	return &fetchCmd{
		RootCmd: rootCmd,
		todo:    make(chan entities.Hash),
	}
}

func (c *fetchCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "fetch",
		Usage:   "[-c] [-t] [-b] [<commit|tree|path>]",
		Aliases: []string{},
		Desc: `Fetch objects reachable from the provided commit, tree, or directory from the CAS store.
If <commit|tree|path> is not provided, the SHA will be inferred by the current working directory. When '-c' is provided, the HEAD commit of the git repository is used. Otherwise, the casfs directory hash is used.`,
	}
}

func (c *fetchCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.fetchCommits, "commits", "c", false, "Set to fetch reachable commit objects")
	fl.BoolVarP(&c.fetchTrees, "trees", "t", false, "Set to fetch reachable tree objects")
	fl.BoolVarP(&c.fetchBlobs, "blobs", "b", false, "Set to fetch reachable blob objects. This option implies -t")
	fl.IntVarP(&c.jobs, "jobs", "j", runtime.NumCPU(), "Number of concurrent fetches to run")
}

type inferMode int

const (
	commitMode inferMode = iota
	treeMode
)

// inferSHA uses the provided directory to infer the SHA to use.
// In commit mode, the HEAD commit is used. In tree mode, the directory hash
// is used.
func inferSHA(mode inferMode, dir string) (string, error) {
	switch mode {
	case commitMode:
		gitRepo, err := git.PlainOpen(dir)
		if err != nil {
			return "", err
		}
		hash, err := gitRepo.ResolveRevision(plumbing.Revision("HEAD"))
		if err != nil {
			return "", err
		}
		return hash.String(), nil
	case treeMode:
		h, err := bindcas.Hash(dir)
		if err != nil {
			return "", err
		}
		return h.String(), nil
	default:
		return "", fmt.Errorf("unsupported mode %d", mode)
	}
}

func (c *fetchCmd) populateFrom(arg string) error {
	mode := treeMode
	if c.fetchCommits {
		mode = commitMode
	}

	// If an arg is provided, it can be a hash or a path to a directory.
	if arg != "" {
		_, err := os.Stat(arg)
		// Not a file, treat it as a hash directly.
		if err != nil {
			c.from = arg
			return nil
		}

		c.from, err = inferSHA(mode, arg)
		return err
	}

	// Otherwise, use current working directory.
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	c.from, err = inferSHA(mode, cwd)
	return err
}

func (c *fetchCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	arg := args.EnsureSingleArg(fl)
	if err := c.populateFrom(arg); err != nil {
		return clierr.CLIErr("Failed to populate from %s: %v", arg, err)
	}

	return c.run(ctx)
}

func (c *fetchCmd) run(ctx context.Context) error {
	if !c.fetchBlobs && !c.fetchCommits && !c.fetchTrees {
		return clierr.CLIErr("At least one of blobs, commits, or trees must be specified for fetching.")
	}

	// Force the fetching of trees if blobs are specified so that inner trees
	// are recursed
	if c.fetchBlobs {
		c.fetchTrees = true
	}

	hash := entities.NewHash(c.from)

	if err := c.fetchReachableObjects(ctx, hash); err != nil {
		return fmt.Errorf("failed to fetch reachable objects from %v. err: %v", hash, err)
	}

	var sb strings.Builder
	sb.WriteString("Fetching complete.\n")
	fmt.Fprintf(&sb, "%-20s %d\n", "Commits Fetched:", c.commitCnt.Load())
	fmt.Fprintf(&sb, "%-20s %d\n", "Trees Fetched:", c.treeCnt.Load())
	fmt.Fprintf(&sb, "%-20s %d\n", "Blobs Fetched:", c.blobCnt.Load())

	log.Println(sb.String())
	return nil
}

func (c *fetchCmd) fetchReachableObjects(ctx context.Context, hash entities.Hash) error {
	_, _ = ctx, hash
	// FIXME: Fix after git-pack is implemented

	// objects, err := c.client.FetchObjectsStreaming(ctx, hash, mask)
	// if err != nil {
	// 	return err
	// }

	// var eg errgroup.Group
	// eg.Go(func() error {
	// 	return c.bufferObjects(ctx, objects)
	// })

	// eg.Go(func() error {
	// 	return c.writeBufferedObjects(ctx)
	// })

	// return eg.Wait()
	return nil
}

// func (c *cmd) handleObjectReadCloser(ctx context.Context, object remote.ObjectReadCloser) error {
// 	if errReadCloser, ok := object.Data.(*remote.ErrorReadCloser); ok {
// 		return errReadCloser.Err
// 	}
// 	defer object.Data.Close()

// 	b, err := io.ReadAll(object.Data)
// 	if err != nil {
// 		return err
// 	}
// 	buffer := bytes.NewBuffer(b)
// 	c.bufferedObjects <- objectReader{hash: object.Hash, objType: object.ObjType, reader: buffer}
// 	return nil
// }

// func (c *cmd) writeBufferedObject(ctx context.Context, object objectReader) error {
// 	switch object.objType {
// 	case gitobjects.ObjectTypeCommit:
// 		if err := c.store.WriteObject(ctx, object.hash, object.reader); err != nil {
// 			return err
// 		}
// 		c.commitCnt.Add(1)
// 	case gitobjects.ObjectTypeDir:
// 		if err := c.store.WriteObject(ctx, object.hash, object.reader); err != nil {
// 			return err
// 		}
// 		c.treeCnt.Add(1)
// 	case binding.ObjectTypeBlob:
// 		if err := c.store.WriteBlob(ctx, object.hash, object.reader); err != nil {
// 			return err
// 		}
// 		c.blobCnt.Add(1)
// 	default:
// 		return fmt.Errorf("unsupported object type %s", object.objType)
// 	}

// 	return nil
// }

// func (c *cmd) bufferObjects(ctx context.Context, objects <-chan remote.ObjectReadCloser) error {
// 	// On an early exit, the objects channel may not be drained, and it holds
// 	// resources open that need to be closed.
// 	defer func() {
// 		for object := range objects {
// 			if _, ok := object.Data.(*remote.ErrorReadCloser); ok {
// 				continue
// 			}
// 			object.Data.Close()
// 		}
// 	}()
// 	defer close(c.bufferedObjects)

// 	for object := range objects {
// 		if err := c.handleObjectReadCloser(ctx, object); err != nil {
// 			return err
// 		}
// 	}

// 	return nil
// }

// func (c *cmd) writeBufferedObjects(ctx context.Context) error {
// 	var eg errgroup.Group
// 	eg.SetLimit(runtime.NumCPU())
// 	for object := range c.bufferedObjects {
// 		object := object

// 		eg.Go(func() error {
// 			return c.writeBufferedObject(ctx, object)
// 		})
// 	}

// 	return eg.Wait()
// }

// FetchObjectsStreaming returns all reachable objects from the input key.
// Callers MUST drain the channel and Close() the `Data` field to avoid resource
// leaks.
// func (s *fileBasedServer) FetchObjectsStreaming(ctx context.Context, hash Hash, mask remote.ReachableObjectsMask) (<-chan remote.ObjectReadCloser, error) {
// 	decoder := func(ctx context.Context, hash Hash) (binding.ObjectType, binding.Object, error) {
// 		return objectFromHash(ctx, s.cache, hash)
// 	}
// 	opener := func(ctx context.Context, hash Hash, objType binding.ObjectType) (io.ReadCloser, error) {
// 		var path string
// 		if objType == binding.ObjectTypeBlob {
// 			path = s.cache.BlobPath(ctx, hash)
// 		} else {
// 			path = s.cache.ObjectPath(ctx, hash)
// 		}
// 		return os.Open(path)
// 	}

// 	w := newWalker(hash, mask, decoder)
// 	walkerStream, err := w.stream()
// 	if err != nil {
// 		return nil, err
// 	}

// 	outCh := make(chan remote.ObjectReadCloser)
// 	go func() {
// 		defer close(outCh)

// 		for item := range walkerStream {
// 			readCloser, err := opener(ctx, item.hash, item.objType)
// 			if err != nil {
// 				outCh <- remote.ObjectReadCloser{
// 					Data: &remote.ErrorReadCloser{Err: err},
// 				}
// 				return
// 			}

// 			outCh <- remote.ObjectReadCloser{
// 				Hash:    item.hash,
// 				ObjType: item.objType,
// 				Data:    readCloser,
// 			}
// 		}
// 	}()

// 	return outCh, nil
// }
