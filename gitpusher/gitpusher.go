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

package gitpusher

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/ssgelm/cookiejarparser"

	shellgit "github.com/google/abfs/git"
	"github.com/google/abfs/remoteconfig"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/hashpath"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/remote"
	abfsclient "github.com/google/abfs/shared/remote/grpc/client"
)

const repoForStandalones = ""

type GitPusher struct {
	instanceName       string
	configHP           *hashpath.HashPath
	gitBin             string
	projectStoragePath string
	servers            []string
	authInfo           instance.AuthInfo
	shgit              *shellgit.ShellGit

	startTime     time.Time
	abfsClient    *abfsclient.ABFSClient
	limitedRemote *remote.LimitedRemoteProvider
	projectSched  *pushersScheduler
	repoSched     *repoScheduler
	poolConfig    *GitPusherPoolConfig
	pusherConfig  *GitPusherConfig
	httpGitPusher *httpGitPusher
}

type NewGitPusherArgs struct {
	InstanceName string
	ConfigHP     *hashpath.HashPath
	GitBin       string
	StoragePath  string
	Servers      []string
	AuthInfo     instance.AuthInfo
}

func NewGitPusher(args *NewGitPusherArgs) *GitPusher {
	return &GitPusher{
		instanceName:       args.InstanceName,
		configHP:           args.ConfigHP,
		gitBin:             args.GitBin,
		projectStoragePath: args.StoragePath,
		servers:            args.Servers,
		authInfo:           args.AuthInfo,
		shgit:              &shellgit.ShellGit{BinPath: args.GitBin},
	}
}

func (g *GitPusher) Run(ctx context.Context) error {
	g.startTime = time.Now()

	client, err := abfsclient.NewABFSClient(&abfsclient.ABFSClientOpts{
		Servers:  g.servers,
		AuthInfo: g.authInfo,
	})
	if err != nil {
		return err
	}
	g.abfsClient = client

	if err := setupGitCookies(g.gitBin); err != nil {
		return err
	}

	remoteconfig.WatchConfig(ctx, g.configHP, remote.NewRemoteStore(g.abfsClient), func(config *GitPusherPoolConfig, configHash entities.Hash) {
		if err := g.handleConfigChange(config, configHash); err != nil {
			log.Printf("failed to handle config change: %v", err)
			return
		}
	})
	return nil
}

func (g *GitPusher) handleConfigChange(newConfig *GitPusherPoolConfig, configHash entities.Hash) error {
	start := time.Now()
	log.Printf("Applying new config: %s", configHash)

	thisPusher, err := newConfig.Instance(g.instanceName)
	if err != nil {
		return err
	}

	if thisPusher.Inactive {
		return fmt.Errorf("pusher %s is inactive", g.instanceName)
	}

	if err := g.handleHTTPServer(thisPusher); err != nil {
		return err
	}

	if g.limitedRemote == nil {
		g.limitedRemote = remote.NewLimitedRemote(g.abfsClient, thisPusher.MaxRemotePushes)
	} else {
		g.limitedRemote.SetLimit(thisPusher.MaxRemotePushes)
	}

	if g.projectSched == nil {
		g.projectSched = newPushersScheduler(*thisPusher.Index, newConfig.PushersCount, thisPusher.MaxProjectsSyncs, thisPusher.MaxMemoryUsage, g.newProjectPusher)
	} else {
		g.projectSched.SetMaxSyncs(thisPusher.MaxProjectsSyncs)
		g.projectSched.SetMaxMemoryUsage(thisPusher.MaxMemoryUsage)
		g.projectSched.SetIndex(*thisPusher.Index, newConfig.PushersCount)
	}

	if g.repoSched == nil {
		g.repoSched = newRepoScheduler(g.newProjectLister, g.projectSched)
	}

	g.handleReposChange(newConfig)

	g.handleProjectsChange(newConfig)

	g.poolConfig = newConfig
	g.pusherConfig = thisPusher
	log.Printf("Config updated %s in %s", configHash, time.Since(start))
	return nil
}

func (g *GitPusher) handleProjectsChange(newConfig *GitPusherPoolConfig) {
	var oldProjects []*ProjectConfig
	if g.poolConfig != nil {
		oldProjects = g.poolConfig.Projects
	}
	add, remove, update := diffByKey(oldProjects, newConfig.Projects, func(p *ProjectConfig) string {
		return p.Name().String()
	})

	// We pass repoForStandalones as the repo name for standalone projects.
	// This is because standalone projects are not associated with any repo.
	for _, proj := range add {
		if err := g.projectSched.AddProject(proj, repoForStandalones); err != nil {
			log.Printf("Failed to add project %q: %v", proj.Name(), err)
		}
	}
	for _, proj := range remove {
		if err := g.projectSched.RemoveProject(proj, repoForStandalones); err != nil {
			log.Printf("Failed to remove project %q: %v", proj.Name(), err)
		}
	}
	for _, proj := range update {
		if err := g.projectSched.UpdateProject(proj, repoForStandalones); err != nil {
			log.Printf("Failed to update project %q: %v", proj.Name(), err)
		}
	}
}

func (g *GitPusher) handleReposChange(newConfig *GitPusherPoolConfig) {

	var oldRepos []*RepoConfig
	if g.poolConfig != nil {
		oldRepos = g.poolConfig.Repos
	}
	add, remove, update := diffByKey(oldRepos, newConfig.Repos, func(r *RepoConfig) string {
		return r.Name().String()
	})
	for _, r := range add {
		if err := g.repoSched.AddRepo(r); err != nil {
			log.Printf("Failed to add repo %q: %v", r.Name(), err)
		}
	}
	for _, r := range remove {
		if err := g.repoSched.RemoveRepo(r); err != nil {
			log.Printf("Failed to remove repo %q: %v", r.Name(), err)
		}
	}
	for _, r := range update {
		if err := g.repoSched.UpdateRepo(r); err != nil {
			log.Printf("Failed to update repo %q: %v", r.Name(), err)
		}
	}
}

func (g *GitPusher) newProjectPusher(config *ProjectConfig) Pusher {
	return newPusher(config, g, g.instanceName)
}

func (g *GitPusher) newProjectLister(config *RepoConfig) ProjectLister {
	return newRepoWatcher(config, g)
}

func (g *GitPusher) handleHTTPServer(thisPusher *GitPusherConfig) error {
	var prevPort int
	if g.pusherConfig != nil {
		prevPort = g.pusherConfig.Port
	}
	if prevPort == thisPusher.Port {
		return nil
	}
	if g.httpGitPusher != nil {
		if err := g.httpGitPusher.stop(); err != nil {
			log.Printf("failed to stop http server: %v", err)
		}
	}
	server, err := newHTTPGitPusher(thisPusher.Port, g.pusherConfig, g)
	if err != nil {
		return err
	}
	go func() {
		if err := server.run(); err != nil {
			log.Printf("failed to run http server: %v", err)
		}
	}()
	g.httpGitPusher = server
	return nil
}

func (g *GitPusher) SyncDump(ctx context.Context, projName ProjectName, w io.Writer) error {
	return g.projectSched.SyncDump(ctx, projName, w)
}

func (g *GitPusher) SyncValidate(ctx context.Context, projName ProjectName, w io.Writer, nodeStaleTimeout time.Duration) error {
	return g.projectSched.SyncValidate(ctx, projName, w, nodeStaleTimeout)
}

// setupGitCookies does a one-time setup to ensure the git requests from go-git include the user's
// cookies. This is only necessary if go-git is used to talk to the server.
func setupGitCookies(gitBin string) error {
	b, err := exec.Command(gitBin, "config", "http.cookiefile").Output()
	if err != nil {
		// user may not have cookies
		return nil
	}
	gitCookiePath := strings.TrimSpace(string(b))

	// TODO: this might need revised once we use the service account for auth
	cookies, err := cookiejarparser.LoadCookieJarFile(os.ExpandEnv(gitCookiePath))
	if err != nil {
		return fmt.Errorf("failed to load git cookies from %s: %s", gitCookiePath, err)
	}

	customClient := &http.Client{
		Jar: cookies,
	}

	client.InstallProtocol("https", githttp.NewClient(customClient))
	return nil
}
