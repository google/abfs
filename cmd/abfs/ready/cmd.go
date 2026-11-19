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

package ready

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/casgit"
	"github.com/google/abfs/manifest"
	"github.com/google/abfs/manifest/resolve"
	"github.com/google/abfs/repo"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/hashpath"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/grpc/client"
)

const (
	reasonManifestNotFound = "manifest branch not found"
	reasonManifestIsTag    = "manifest branch not found, only a tag of that name"
	reasonNoProjects       = "no project selected"
	reasonRevisionNotFound = "revision not found"
	reasonCommitNotFound   = "commit not found"
	reasonTimedOut         = "timed out"
)

type readyCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	branch               string
	branchFile           string
	groups               []string
	output               string
	pusherConfigHashPath string
	jsonOut              bool
	wait                 bool
	timeout              time.Duration
	interval             time.Duration
}

// report is the readiness of one manifest file on one manifest branch.
type report struct {
	Branch         string       `json:"branch"`
	File           string       `json:"file"`
	ManifestRef    string       `json:"manifest_ref,omitempty"`
	ManifestCommit string       `json:"manifest_commit,omitempty"`
	Ready          bool         `json:"ready"`
	Blocked        bool         `json:"blocked"`
	Total          int          `json:"total"`
	Resolved       int          `json:"resolved"`
	Unresolved     []unresolved `json:"unresolved"`

	PusherConfigError string `json:"pusher_config_error,omitempty"`

	pinned *manifest.Manifest
}

// unresolved is a manifest branch or a selected project that a mount cannot resolve yet.
type unresolved struct {
	Project  string   `json:"project,omitempty"`
	Path     string   `json:"path,omitempty"`
	Revision string   `json:"revision,omitempty"`
	Groups   []string `json:"groups,omitempty"`
	URL      string   `json:"url,omitempty"`
	Reason   string   `json:"reason"`
	Detail   string   `json:"detail,omitempty"`
	Blocked  bool     `json:"blocked,omitempty"`
}

type opts struct {
	manifestServer      string
	manifestProjectName string
	manifestScheme      string
	branch              string
	branchFile          string
	groups              []string
	pusherConfig        *hashpath.HashPath
}

func NewCmd(rootCmd *root.RootCmd) *readyCmd {
	return &readyCmd{RootCmd: rootCmd}
}

func (c *readyCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "ready",
		Usage:   "-b <branch> [--branch-file <file>] [-g <groups>] [-o <pinned.xml>] [--pusher-config <hash-path>] [--json] [--wait [--timeout <d>] [--interval <d>]]",
		Aliases: []string{},
		Desc: `Report whether every project of a manifest branch is on the server, so a mount of it resolves.
Each missing project gets a cause from the git-pusher config and push status (see --pusher-config);
--wait stops early when only a config change can make the branch ready.
Exits 0 when ready, 1 when not (also when --timeout runs out), 2 on other errors, including an interrupt.
With -o, writes the pinned manifest when ready and removes the file when not. Mount it with the same
-b and -g: "abfs mount -b <branch> -g <groups> -f <pinned.xml>", with or without --ws, and without --local-manifest-dir.`,
	}
}

func (c *readyCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.branch, "branch", "b", "", "Branch to find manifest from")
	fl.StringVarP(&c.branchFile, "branch-file", "", "default.xml", "Manifest file to use from the manifest branch")
	fl.StringSliceVarP(&c.groups, "groups", "g", nil, "Groups to filter. Same as repo init -g")
	fl.StringVarP(&c.output, "output", "o", "", "Write the pinned manifest to this file when ready")
	fl.StringVar(&c.pusherConfigHashPath, "pusher-config", "config:git-pusher/default", "The hash-path of the git-pusher config, used to explain missing projects")
	fl.BoolVar(&c.jsonOut, "json", false, "Output the report as JSON")
	fl.BoolVar(&c.wait, "wait", false, "Check again until ready or the timeout")
	fl.DurationVar(&c.timeout, "timeout", time.Hour, "How long to check, across all rounds with --wait, before reporting not ready")
	fl.DurationVar(&c.interval, "interval", 30*time.Second, "Time between checks with --wait")
}

func (c *readyCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	if c.branch == "" {
		fl.Usage()
		return clierr.CLIErrWithExitCode(2, "missing --branch")
	}
	// abfs mount -b needs the manifest branch under refs/heads/.
	if strings.HasPrefix(c.branch, "refs/") {
		return clierr.CLIErrWithExitCode(2, "--branch takes a branch name, not a ref: %s", c.branch)
	}
	if c.interval <= 0 || c.timeout <= 0 {
		return clierr.CLIErrWithExitCode(2, "--interval and --timeout must be positive")
	}
	servers := c.Config.EffectiveRemoteServers()
	if len(servers) == 0 {
		return clierr.CLIErrWithExitCode(2, "remote address is required")
	}
	pusherConfig, err := hashpath.Parse(c.pusherConfigHashPath)
	if err != nil {
		return clierr.CLIErrWithExitCode(2, "invalid --pusher-config: %v", err)
	}

	o := opts{
		manifestServer:      c.Config.ManifestServer,
		manifestProjectName: c.Config.ManifestProjectName,
		manifestScheme:      c.Config.ManifestScheme,
		branch:              c.branch,
		branchFile:          c.branchFile,
		groups:              c.groups,
		pusherConfig:        pusherConfig,
	}

	// The client connects inside the check, so --wait retries an unreachable server.
	var server *client.ABFSClient
	defer func() {
		if server != nil {
			_ = server.Close()
		}
	}()
	err = c.run(ctx, func(ctx context.Context) (*report, error) {
		if server == nil {
			cli, err := client.NewABFSClient(&client.ABFSClientOpts{
				Servers:  servers,
				AuthInfo: c.Config.GetAuthInfo(),
			})
			if err != nil {
				return nil, err
			}
			server = cli
		}
		return check(ctx, server, o)
	})
	if ctx.Err() != nil {
		// The CLI exits 0 for any command stopped by a signal, which CI would read as ready.
		// Exiting here skips the deferred cleanup, which a process that ends now can do without.
		fmt.Fprintf(os.Stderr, "Interrupted: %v\n", ctx.Err())
		os.Exit(2)
	}
	return err
}

func (c *readyCmd) run(ctx context.Context, check func(context.Context) (*report, error)) error {
	var r *report
	var err error
	timedOut := false
	var configWarning string
	deadline := time.Now().Add(c.timeout)
	for {
		checkCtx, cancel := context.WithDeadline(ctx, deadline)
		r, err = check(checkCtx)
		timedOut = err != nil && checkCtx.Err() != nil
		cancel()
		if err == nil && r.PusherConfigError != "" && r.PusherConfigError != configWarning {
			configWarning = r.PusherConfigError
			log.Printf("Warning: missing projects show no cause: %s", configWarning)
		}
		if ctx.Err() != nil {
			return clierr.CLIErrWithExitCode(2, "%v", ctx.Err())
		}
		if !c.wait || timedOut || (err == nil && (r.Ready || r.Blocked)) || !time.Now().Before(deadline) {
			break
		}
		if err != nil {
			log.Printf("Warning: %v", err)
		} else {
			log.Printf("Not ready: %d of %d projects resolve", r.Resolved, r.Total)
		}
		select {
		case <-ctx.Done():
			return clierr.CLIErrWithExitCode(2, "%v", ctx.Err())
		case <-time.After(c.interval):
		}
		// A round started after the deadline would fail at once and hide the last answer.
		if !time.Now().Before(deadline) {
			break
		}
	}
	// A check cut off by --timeout has not shown the branch ready in time, as when the rounds run out.
	if timedOut {
		log.Printf("Warning: %v", err)
		r = &report{Branch: c.branch, File: c.branchFile, Unresolved: []unresolved{{Reason: reasonTimedOut}}}
		err = nil
	}
	if err != nil {
		return clierr.CLIErrWithExitCode(2, "%v", err)
	}

	if c.output != "" {
		if r.Ready {
			err = writePinned(c.output, r.pinned)
		} else {
			err = removePinned(c.output)
		}
		if err != nil {
			return clierr.CLIErrWithExitCode(2, "failed to update %s: %v", c.output, err)
		}
	}
	if err := c.print(r); err != nil {
		return clierr.CLIErrWithExitCode(2, "%v", err)
	}
	if !r.Ready {
		return clierr.CLIErrNoMessage(1)
	}
	return nil
}

func (c *readyCmd) print(r *report) error {
	if c.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	if r.ManifestCommit != "" {
		fmt.Printf("Manifest %s at %s (%s)\n", r.File, r.ManifestRef, r.ManifestCommit)
	}
	for _, u := range r.Unresolved {
		var fields []string
		for _, field := range []string{u.Project, u.Revision, strings.Join(u.Groups, ",")} {
			if field != "" {
				fields = append(fields, field)
			}
		}
		line := u.Reason
		if len(fields) > 0 {
			line += ": " + strings.Join(fields, " ")
		}
		if u.URL != "" {
			line += " at " + u.URL
		}
		if u.Detail != "" {
			line += " (" + u.Detail + ")"
		}
		fmt.Println(line)
	}
	state := "Ready"
	if r.Blocked {
		state = "Not ready until the git-pusher config changes"
	} else if !r.Ready {
		state = "Not ready"
	}
	fmt.Printf("%s: %d of %d projects resolve\n", state, r.Resolved, r.Total)
	return nil
}

// writePinned writes aside and renames, so a mount never reads a partly written file.
func writePinned(path string, pinned *manifest.Manifest) error {
	out, err := xml.MarshalIndent(pinned, "", " ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func removePinned(path string) error {
	info, err := os.Lstat(path)
	if entities.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	return os.Remove(path)
}

// boundProvider runs the server reads of a check under its context; RemoteStore and its CAS view pass none.
// Only the reads a check makes are bound; a new call on the check path must be added here.
type boundProvider struct {
	remote.RemoteCloser
	ctx context.Context
}

func (p *boundProvider) Headers(_ context.Context, keys []entities.Hash) ([]entities.ObjectHeader, error) {
	return p.RemoteCloser.Headers(p.ctx, keys)
}

func (p *boundProvider) GetObject(_ context.Context, key entities.Hash) (remote.ContentReadCloser, error) {
	return p.RemoteCloser.GetObject(p.ctx, key)
}

func (p *boundProvider) GetRefs(_ context.Context, server string, project string, refs []string) ([]entities.Hash, error) {
	return p.RemoteCloser.GetRefs(p.ctx, server, project, refs)
}

func (p *boundProvider) SearchRefs(_ context.Context, server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	return p.RemoteCloser.SearchRefs(p.ctx, server, project, refPrefix, limit)
}

// check pins a manifest branch the way abfs mount does, reading the server directly.
func check(ctx context.Context, server remote.RemoteCloser, o opts) (*report, error) {
	server = &boundProvider{RemoteCloser: server, ctx: ctx}
	store := remote.NewRemoteStore(server)
	r := &report{Branch: o.branch, File: o.branchFile, Unresolved: []unresolved{}}

	manifestServer := instance.MaybeRemoveScheme(o.manifestServer)
	refs := []string{"refs/heads/" + o.branch, "refs/tags/" + o.branch}
	hashes, err := store.GetRefs(manifestServer, o.manifestProjectName, refs)
	if err != nil && !entities.IsNotExist(err) {
		return nil, fmt.Errorf("failed to look up manifest branch %s: %w", o.branch, err)
	}
	if len(hashes) == 0 || hashes[0].IsZero() {
		reason := reasonManifestNotFound
		if len(hashes) > 1 && !hashes[1].IsZero() {
			reason = reasonManifestIsTag
		}
		r.Unresolved = append(r.Unresolved, unresolved{
			Revision: o.branch,
			URL:      instance.MaybeAddScheme(manifestServer, o.manifestScheme) + "/" + o.manifestProjectName,
			Reason:   reason,
		})
		explain(ctx, store, o, r)
		return r, nil
	}
	commit := hashes[0]
	r.ManifestRef, r.ManifestCommit = refs[0], commit.String()

	rp := repo.New(store, casgit.New(store), o.manifestServer, o.manifestProjectName, o.manifestScheme, "", false)
	rp.SetGroups(o.groups)
	pinned, missing, err := rp.PinManifestAt(ctx, commit, o.branchFile)
	if err != nil {
		return nil, err
	}
	r.pinned = pinned

	byName := make(map[string][]resolve.Request)
	for _, req := range missing {
		byName[req.Project] = append(byName[req.Project], req)
	}
	var selected []*manifest.Project
	var commits []entities.Hash
	for _, p := range pinned.Project {
		if resolve.MatchGroups(p.Groups, o.groups) {
			selected = append(selected, p)
			if entities.IsHash(p.Revision) {
				commits = append(commits, entities.NewHash(p.Revision))
			}
		}
	}

	// Every pinned commit is checked, since a hash written in the manifest proves nothing.
	present := make(map[entities.Hash]bool, len(commits))
	if len(commits) > 0 {
		headers, err := server.Headers(ctx, commits)
		if err != nil {
			return nil, fmt.Errorf("failed to check project commits: %w", err)
		}
		for i, hdr := range headers {
			present[commits[i]] = !hdr.IsEmpty()
		}
	}

	for _, p := range selected {
		switch {
		case !entities.IsHash(p.Revision):
			u := unresolved{Project: p.Name, Path: p.GetPath(), Revision: p.Revision, Reason: reasonRevisionNotFound}
			if reqs := byName[p.Name]; len(reqs) > 0 {
				u.URL, byName[p.Name] = reqs[0].FetchURL, reqs[1:]
			}
			r.Unresolved = append(r.Unresolved, u)
		case !present[entities.NewHash(p.Revision)]:
			r.Unresolved = append(r.Unresolved, unresolved{Project: p.Name, Path: p.GetPath(), Revision: p.Revision, Reason: reasonCommitNotFound})
		default:
			r.Resolved++
		}
	}
	r.Total = len(selected)
	if r.Total == 0 {
		r.Unresolved = append(r.Unresolved, unresolved{Groups: o.groups, Reason: reasonNoProjects})
	}
	r.Ready = len(r.Unresolved) == 0
	if !r.Ready {
		explain(ctx, store, o, r)
	}
	return r, nil
}
