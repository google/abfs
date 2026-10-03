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

package ws

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"

	"github.com/google/abfs/manifest"

	"github.com/google/abfs/binding"

	mount "github.com/google/abfs/cmd/abfs/casfs-mount"
	"github.com/google/abfs/cmd/abfs/doctor"
	"github.com/google/abfs/daemon"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/user"
	"github.com/google/abfs/store"
)

type MountRepoOpts struct {
	MountPoint             string
	TempWS                 bool
	WSName                 string
	Branch                 string
	ManifestHash           entities.Hash
	ManifestFile           string
	Groups                 []string
	Force                  bool
	RefPrefix              string
	SmartSync              bool
	SmartTag               string
	ManifestServerUsername string
	ManifestServerPassword string
	IsSnapshot             bool
	FallbackToGit          bool
}

type mountRepoCmd struct {
	*wsCmd
	foreground bool
	daemon     *daemon.Daemon
	done       chan error

	MountRepoOpts
	manifestHexHash string
}

func NewMountRepoCmd(rootCmd *root.RootCmd, opts MountRepoOpts) *mountRepoCmd {
	return &mountRepoCmd{
		wsCmd:         &wsCmd{RootCmd: rootCmd},
		done:          make(chan error),
		MountRepoOpts: opts,
	}
}

func newMountRepoCmd(c *wsCmd) *mountRepoCmd {
	return &mountRepoCmd{wsCmd: c, done: make(chan error)}
}

func (c *mountRepoCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "mount-repo",
		Usage:   "[--ws <name> | --tmp-ws] [-b <branch> | -h <manifest-hash> | -f <manifest-file>] <mnt-point>",
		Aliases: []string{},
		Desc:    `Mount the new workspace at the given path`,
		Hidden:  true,
	}
}

func (c *mountRepoCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVar(&c.foreground, "foreground", false, "Run the command in foreground")
	fl.StringVarP(&c.WSName, "ws", "", "", "Workspace name")
	fl.BoolVar(&c.TempWS, "tmp-ws", false, "Create a new workspace")
	fl.StringVarP(&c.Branch, "branch", "b", "", "Branch name")
	fl.StringVarP(&c.manifestHexHash, "manifest-hash", "h", "", "A pinned manifest file hash (blob hash of a manifest xml)")
	fl.StringVarP(&c.ManifestFile, "file", "f", "", "A pinned manifest file (like path to default.xml)")
	fl.StringSliceVarP(&c.Groups, "groups", "g", nil, "Groups to filter. Same as repo init -g")
	fl.BoolVar(&c.Force, "force", false, "Attempt to unmount an existing casfs mount or create the mountpoint")
	fl.StringVarP(&c.RefPrefix, "ref-prefix", "r", "", "Prefix that will be used in the ABFS meta refs for builds and mounts (default is user/$USER)")
	fl.BoolVarP(&c.SmartSync, "smart-sync", "s", false, "Sync to a known good revision as specified by the manifest server")
	fl.StringVarP(&c.SmartTag, "smart-tag", "t", "", "Sync to a tag as specified by the manifest server")
	fl.StringVar(&c.ManifestServerUsername, "manifest-server-username", "", "Username to authenticate with the manifest server")
	fl.StringVar(&c.ManifestServerPassword, "manifest-server-password", "", "Password to authenticate with the manifest server")
	fl.BoolVar(&c.FallbackToGit, "fallback-to-git", false, "Fallback to git operation directly if mount fails on a project.")
}

func (c *mountRepoCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	c.MountPoint = args.EnsureSingleArg(fl)

	if c.WSName == "" && !c.TempWS {
		return clierr.CLIErr("Provide either --ws or --tmp-ws")
	}

	if c.WSName != "" && c.TempWS {
		return clierr.CLIErr("Cannot provide both --ws and --tmp-ws")
	}

	if c.manifestHexHash != "" && c.ManifestFile != "" {
		return clierr.CLIErr("Cannot provide both --manifest-hash and --file")
	}

	if c.manifestHexHash != "" {
		if !entities.IsHash(c.manifestHexHash) {
			return clierr.CLIErr("Invalid manifest hex hash")
		}
		c.ManifestHash = entities.NewHash(c.manifestHexHash)
	}

	if c.ManifestFile != "" {
		absPath, err := filepath.Abs(c.ManifestFile)
		if err != nil {
			return err
		}
		c.ManifestFile = absPath
	}

	if err := doctor.PreflightCheck(c.Instance); err != nil {
		return err
	}

	return c.Run(ctx)
}

func (c *mountRepoCmd) Run(ctx context.Context) error {
	if c.foreground || daemon.IsDaemon() {
		return c.runForeground(ctx)
	}
	return c.startDaemon()
}

func (c *mountRepoCmd) runForeground(ctx context.Context) (retErr error) {
	if daemon.IsDaemon() {
		d, err := daemon.SetupDaemon(filepath.Join(c.Config.LogDir, "abfs"))
		if err != nil {
			return err
		}
		c.daemon = d
	}

	c.initWorkspaces()

	origWSName := c.WSName
	if !c.TempWS && c.WSName == "" {
		wsName, err := discoverWSName(c.MountPoint)
		if err != nil {
			return fmt.Errorf("failed to discover workspace name: %w", err)
		}
		fmt.Printf("Using %q as workspace name.\n", wsName)
		c.WSName = wsName
	}

	if c.WSName != "" {
		if _, err := c.workspaces.FetchWSSnapshot(ctx, c.WSName); err != nil {
			if !entities.IsNotExist(err) {
				return err
			}
		} else {
			wsOpt := ""
			if origWSName != "" {
				wsOpt = fmt.Sprintf(" --ws %s", origWSName)
			}
			return clierr.CLIErr("Workspace %s already exists. Your options are:\n"+
				"  1. Use a different workspace name using `--ws` option\n"+
				"  2. Remove %q workspace and try to mount again: `abfs ws remove %s`\n"+
				"  3. Recover %q workspace: `abfs remount %s%s`",
				c.WSName, c.WSName, c.WSName, c.WSName, c.MountPoint, wsOpt)
		}
	}

	var wsSnapshot *binding.WSSnapshot
	if !c.ManifestHash.IsZero() {
		if c.IsSnapshot {
			if c.Store == nil {
				return fmt.Errorf("store provider is not initialized")
			}
			content, err := c.Store.GetObject(ctx, c.ManifestHash)
			if err != nil {
				return fmt.Errorf("failed to fetch snapshot: %w", err)
			}
			wsSnapshot = new(binding.WSSnapshot)
			if err := wsSnapshot.Decode(c.ManifestHash, content); err != nil {
				return fmt.Errorf("failed to decode snapshot: %w", err)
			}
		} else {
			if err := c.validateManifestHash(ctx); err != nil {
				return err
			}
		}
	} else if c.ManifestFile != "" {
		if err := c.validateManifestFile(); err != nil {
			return err
		}
	}

	c.MountPoint = instance.AbsPath(c.MountPoint)

	if err := c.mount(ctx); err != nil {
		return err
	}

	if err := os.Chdir(c.MountPoint); err != nil {
		return fmt.Errorf("failed to chdir to %s: %w", c.MountPoint, err)
	}

	if c.IsSnapshot {
		if wsSnapshot == nil {
			return fmt.Errorf("workspace snapshot metadata is missing")
		}
		abfsCli, err := c.MountClientProvider.Client(c.MountPoint)
		if err != nil {
			return fmt.Errorf("failed to create abfs client for restore: %w", err)
		}
		defer abfsCli.Close()

		if err := abfsCli.RestoreSnapshot(ctx, wsSnapshot.Snapshot); err != nil {
			return fmt.Errorf("failed to restore snapshot: %w", err)
		}
	} else {
		if !c.ManifestHash.IsZero() {
			if err := c.putPinnedManifest(); err != nil {
				return err
			}
		} else if c.ManifestFile != "" {
			if err := c.putPinnedManifestFromFile(); err != nil {
				return err
			}
		}

		if err := c.repoInit(ctx); err != nil {
			return err
		}

		if err := c.repoSync(ctx); err != nil {
			return err
		}
	}

	if !c.foreground {
		if c.daemon != nil {
			c.daemon.Switch()
		}
	}

	if c.WSName != "" {
		if out, err := exec.CommandContext(ctx, os.Args[0], "ws", "attach", c.WSName, "-n").CombinedOutput(); err != nil {
			return fmt.Errorf("failed to attach workspace: %w\n%s", err, out)
		}
	}

	return <-c.done
}

func discoverWSName(mountPoint string) (string, error) {
	mntPoint, err := filepath.Abs(mountPoint)
	if err != nil {
		return "", err
	}
	currUser, err := user.CurrentUser()
	if err != nil {
		return "", err
	}

	// Check for ~/workspace/...
	if rel, ok := strings.CutPrefix(mntPoint, currUser.HomeDir+string(os.PathSeparator)); ok {
		return rel, nil
	}

	// Check for /abfs/...
	if rel, ok := strings.CutPrefix(mntPoint, "/abfs/"); ok {
		return rel, nil
	}

	return mntPoint[1:], nil
}

func (c *mountRepoCmd) validateManifestHash(ctx context.Context) error {
	content, err := store.ReadAllBlob(ctx, c.Store, c.ManifestHash)
	if err != nil {
		return fmt.Errorf("failed to read manifest %s: %w", c.ManifestHash, err)
	}
	man := new(manifest.Manifest)
	if err := xml.Unmarshal(content, &man); err != nil {
		return err
	}
	// TODO do some other validations of the manifest
	return nil
}

func (c *mountRepoCmd) validateManifestFile() error {
	content, err := os.ReadFile(c.ManifestFile)
	if err != nil {
		return fmt.Errorf("failed to read manifest %s: %w", c.ManifestFile, err)
	}
	man := new(manifest.Manifest)
	if err := xml.Unmarshal(content, &man); err != nil {
		return err
	}
	return nil
}

func (c *mountRepoCmd) pinnedManPath() string {
	return path.Join(c.MountPoint, ".repo", "pinned.xml")
}

func (c *mountRepoCmd) putPinnedManifest() error {
	if err := os.MkdirAll(path.Join(c.MountPoint, ".repo"), 0755); err != nil {
		return err
	}

	if err := bindcas.Mknod(c.pinnedManPath(), entities.FileModeRegular, 0); err != nil {
		return err
	}

	if err := bindcas.SetHash(c.pinnedManPath(), c.ManifestHash); err != nil {
		return err
	}

	return nil
}

func (c *mountRepoCmd) putPinnedManifestFromFile() (retErr error) {
	if err := os.MkdirAll(path.Join(c.MountPoint, ".repo"), 0755); err != nil {
		return err
	}

	src, err := os.Open(c.ManifestFile)
	if err != nil {
		return fmt.Errorf("failed to open manifest file %s: %w", c.ManifestFile, err)
	}
	defer entities.CloseErrCheck(src, &retErr)

	dst, err := os.OpenFile(c.pinnedManPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create pinned manifest: %w", err)
	}
	defer entities.CloseErrCheck(dst, &retErr)

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("failed to copy pinned manifest: %w", err)
	}

	return nil
}

func (c *mountRepoCmd) mount(ctx context.Context) error {
	casfsMountCmd := mount.NewCmd(c.RootCmd)
	fl := pflag.NewFlagSet("casfs-mount", pflag.ContinueOnError)
	casfsMountCmd.RegisterFlags(fl)
	casfsMountCmd.ConfigProvider = c.ConfigProvider
	casfsMountCmd.StoreProvider = c.StoreProvider
	casfsMountCmd.CachemanProvider = c.CachemanProvider

	serving := make(chan struct{})
	if !c.foreground {
		casfsMountCmd.AddCallback(func() error {
			close(serving)
			return nil
		})
	}
	var args []string
	if c.Force {
		args = append(args, "--check-mount=force")
	}
	if c.RefPrefix != "" {
		args = append(args, "--ref-prefix", c.RefPrefix)
	}

	args = append(args, c.MountPoint)
	if err := fl.Parse(args); err != nil {
		return fmt.Errorf("failed to parse mount flags: %w", err)
	}

	go func() {
		defer close(c.done)
		c.done <- casfsMountCmd.Exec(ctx, fl)
	}()

	select {
	case err := <-c.done:
		return err
	case <-serving:
		return nil
	}
}

func (c *mountRepoCmd) repoInit(ctx context.Context) error {
	manURL := new(url.URL)
	manURL.Scheme = c.Config.ManifestScheme
	manURL.Host = c.Config.ManifestServer
	manURL.Path = c.Config.ManifestProjectName

	args := []string{"repo", "init", manURL.String()}

	if len(c.Groups) > 0 {
		args = append(args, "-g", strings.Join(c.Groups, ","))
	}

	if c.Branch != "" {
		args = append(args, "-b", c.Branch)
	}

	if !c.ManifestHash.IsZero() || c.ManifestFile != "" {
		args = append(args, "-m", c.pinnedManPath())
	}

	if c.FallbackToGit {
		args = append(args, "--fallback-to-git")
	}

	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("repo init failed: %w", err)
	}
	return nil
}

func (c *mountRepoCmd) repoSync(ctx context.Context) error {
	args := []string{"repo", "sync"}
	if c.SmartSync {
		args = append(args, "--smart-sync")
	}
	if c.SmartTag != "" {
		args = append(args, "--smart-tag", c.SmartTag)
	}
	if c.ManifestServerUsername != "" {
		args = append(args, "--manifest-server-username", c.ManifestServerUsername)
	}
	if c.ManifestServerPassword != "" {
		args = append(args, "--manifest-server-password", c.ManifestServerPassword)
	}
	if c.FallbackToGit {
		args = append(args, "--fallback-to-git")
	}

	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("repo sync failed: %w", err)
	}
	return nil
}

func (c *mountRepoCmd) startDaemon() error {
	args := os.Args[1:]
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), daemon.ABFSWSMountDaemonEnvVar+"=1")
	return daemon.ForkDaemon(cmd)
}

func (c *mountRepoCmd) CloseDaemon() error {
	if c.daemon != nil {
		return c.daemon.Close()
	}
	return nil
}
