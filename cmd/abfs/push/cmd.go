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

package push

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/schollz/progressbar/v3"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	shellgit "github.com/google/abfs/git"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	abfsclient "github.com/google/abfs/shared/remote/grpc/client"
)

type handleFn func(obj *shellgit.ObjectTypeName) error

type pushCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	*cmd.StoreProvider
	listFile        string
	repoPath        string
	skipNegotiation bool
	shallow         bool
	verbose         bool
}

func NewCmd(rootCmd *root.RootCmd) *pushCmd {
	return &pushCmd{RootCmd: rootCmd}
}

func (c *pushCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "push",
		Usage:   "[-f <path to repo list file>] [-r <path to git repo>] [--shallow] [--skip-negotiation] [--verbose]",
		Aliases: []string{},
		Desc: `Push objects from one or more existing git repositories.

	If providing a repo list file, paths must be full paths separated by a newline.
	'-' may be used to provide repo list to stdin.`,
	}
}

func (c *pushCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVar(&c.verbose, "verbose", false, "Display every push")
	fl.StringVarP(&c.listFile, "file", "f", "", "Path to repo list file")
	fl.StringVarP(&c.repoPath, "repo", "r", "", "Path to git repo")
	fl.BoolVar(&c.skipNegotiation, "skip-negotiation", false, "When set, do not query server for present objects")
	fl.BoolVar(&c.shallow, "shallow", false, "When set, only push objects necessary to construct the HEAD")
}

type pathAndRepo struct {
	path    string
	gitRepo *git.Repository
}

func reposFromReader(r io.Reader) []pathAndRepo {
	scanner := bufio.NewScanner(r)

	var repos []pathAndRepo
	for scanner.Scan() {
		gitRepo, err := git.PlainOpen(scanner.Text())
		if err != nil {
			log.Fatalf("Error: %v", err)
		}
		repos = append(repos, pathAndRepo{scanner.Text(), gitRepo})
	}
	if scanner.Err() != nil {
		log.Fatalf("Error: unable to scan input : %v", scanner.Err())
	}

	return repos
}

func (c *pushCmd) reposFromArgs() []pathAndRepo {
	if c.repoPath != "" {
		gitRepo, err := git.PlainOpen(c.repoPath)
		if err != nil {
			log.Fatalf("Error: %v", err)
		}
		return []pathAndRepo{{c.repoPath, gitRepo}}
	}

	// From stdin.
	if c.listFile == "-" {
		return reposFromReader(os.Stdin)
	}

	// From a filelist.
	file, err := os.Open(c.listFile)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			log.Fatalf("Error: %v", err)
		}
	}()

	return reposFromReader(file)
}

func (c *pushCmd) shallowObjects(dir string, gitRepo *git.Repository) (<-chan shellgit.ObjectTypeNameStreamItem, int64, error) {
	shellGit := &shellgit.ShellGit{BinPath: "git"}

	objs, err := shellGit.ObjectsInTree(dir, "HEAD")
	if err != nil {
		return nil, 0, err
	}

	commitHash, err := gitRepo.ResolveRevision(plumbing.Revision("HEAD"))
	if err != nil {
		return nil, 0, err
	}
	objs = append(objs, &shellgit.ObjectTypeName{ObjType: plumbing.CommitObject, Name: entities.Hash(*commitHash)})

	commit, err := object.GetCommit(gitRepo.Storer, *commitHash)
	if err != nil {
		return nil, 0, err
	}
	objs = append(objs, &shellgit.ObjectTypeName{ObjType: plumbing.TreeObject, Name: entities.Hash(commit.TreeHash)})

	out := make(chan shellgit.ObjectTypeNameStreamItem)
	go func() {
		for _, obj := range objs {
			out <- shellgit.ObjectTypeNameStreamItem{
				ObjectTypeName: obj,
			}
		}
		close(out)
	}()

	return out, int64(len(objs)), nil
}

func (c *pushCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	if c.listFile == "" && c.repoPath == "" {
		return clierr.CLIErr("Error: Either '-f' or '-r' must be specified.")
	}
	if c.listFile != "" && c.repoPath != "" {
		return clierr.CLIErr("Error: Both '-f' or '-r' are specified.")
	}

	var (
		client *abfsclient.ABFSClient
	)
	if !c.Config.LocalOnly && !c.skipNegotiation {
		servers := c.Config.EffectiveRemoteServers()
		if len(servers) == 0 {
			log.Fatal("Error: remote address is required")
		}
		var err error
		client, err = abfsclient.NewABFSClient(&abfsclient.ABFSClientOpts{
			Servers:  servers,
			AuthInfo: c.Config.GetAuthInfo(),
		})
		if err != nil {
			log.Fatalf("Error: unable to start abfsclient: %v", err)
		}
	}

	repos := c.reposFromArgs()
	eg := new(errgroup.Group)
	eg.SetLimit(runtime.NumCPU())

	bar := progressbar.Default(int64(len(repos)))
	for _, r := range repos {
		path := r.path
		gitRepo := r.gitRepo

		eg.Go(func() error {
			defer func() {
				_ = bar.Add(1)
			}()

			hash, err := gitRepo.ResolveRevision(plumbing.Revision("HEAD"))
			if err != nil {
				log.Printf("Error resolving HEAD for %q: %v", path, err)
				return fmt.Errorf("path: %q, err: %w", path, err)
			}

			var excludeList []entities.Hash
			if !c.Config.LocalOnly && !c.skipNegotiation {
				log.Printf("Calculating exclude set for %q...\n", path)
				excludeList, err = excludes(gitRepo, client)
				if err != nil {
					log.Printf("Error calculating exclude set for %q: %v", path, err)
					return fmt.Errorf("path: %q, err: %w", path, err)
				}
			}

			start := time.Now()
			handler := c.handler(NewPusher(gitRepo, c.Store))
			crawler := NewCrawler(path, entities.Hash(*hash), excludeList)

			var (
				expectCnt int64
				ch        <-chan shellgit.ObjectTypeNameStreamItem
			)

			if c.shallow {
				ch, expectCnt, err = c.shallowObjects(path, gitRepo)
				if err != nil {
					log.Printf("Error: %q: %v", path, err)
					return fmt.Errorf("path: %q, err: %w", path, err)
				}
			} else {
				log.Printf("Determining amount of new objects for %q...\n", path)
				expectCnt, err = crawler.count()
				if err != nil {
					log.Printf("Error: %q: %v", path, err)
					return fmt.Errorf("path: %q, err: %w", path, err)
				}

				ch, err = crawler.Objects()
				if err != nil {
					log.Printf("Error: %q: %v", path, err)
					return fmt.Errorf("path: %q, err: %w", path, err)
				}
			}
			log.Printf("Streaming %d objects for %q...\n", expectCnt, path)

			for item := range ch {
				if item.Err != nil {
					log.Printf("Error: %q: %v", path, item.Err)
					return fmt.Errorf("path: %q, err: %w", path, item.Err)
				}
				if err := handler(item.ObjectTypeName); err != nil {
					log.Printf("Error: %q: %v", path, err)
					return fmt.Errorf("path: %q, err: %w", path, err)
				}
			}

			log.Printf("Completed push of %d objects for %q in %v\n", expectCnt, path, time.Since(start))
			return nil
		})
	}
	return eg.Wait()
}

func (c *pushCmd) handler(pusher *Pusher) handleFn {
	if c.verbose {
		return func(obj *shellgit.ObjectTypeName) error {
			fmt.Printf("%v %v\n", obj.ObjType, obj.Name)
			return pusher.Push(obj)
		}
	}

	return func(obj *shellgit.ObjectTypeName) error {
		return pusher.Push(obj)
	}
}
