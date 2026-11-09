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
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	mount "github.com/google/abfs/cmd/abfs/casfs-mount"
	"github.com/google/abfs/cmd/abfs/doctor"
	"github.com/google/abfs/daemon"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

type MountOpts struct {
	MountPoint string
	WSName     string
	Foreground bool
}

type mountCmd struct {
	*wsCmd
	MountOpts
	daemon *daemon.Daemon
	done   chan error
}

func NewMountCmd(rootCmd *root.RootCmd, opts MountOpts) *mountCmd {
	return &mountCmd{
		wsCmd:     &wsCmd{RootCmd: rootCmd},
		MountOpts: opts,
		done:      make(chan error),
	}
}

func newMountCmd(c *wsCmd) *mountCmd {
	return &mountCmd{wsCmd: c, done: make(chan error)}
}

func (c *mountCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "mount",
		Usage:   "<workspace> <path>",
		Aliases: []string{},
		Desc:    `Mount an existing workspace at the given path`,
		Hidden:  true,
	}
}

func (c *mountCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVar(&c.Foreground, "foreground", false, "Run the command in foreground")
}

func (c *mountCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	c.WSName, c.MountPoint = args.EnsureTwoArgs(fl)

	if err := doctor.PreflightCheck(c.Instance); err != nil {
		return err
	}

	return c.Run(ctx)
}

func (c *mountCmd) Run(ctx context.Context) error {
	c.initWorkspaces()

	if c.Foreground || daemon.IsDaemon() {
		return c.runForeground(ctx)
	}
	return c.startDaemon()
}

func (c *mountCmd) runForeground(ctx context.Context) error {
	if daemon.IsDaemon() {
		d, err := daemon.SetupDaemon(filepath.Join(c.Config.LogDir, "abfs"))
		if err != nil {
			return err
		}
		c.daemon = d
	}

	if c.WSName == "" {
		wsName, err := discoverWSName(c.MountPoint)
		if err != nil {
			return fmt.Errorf("failed to discover workspace name: %w", err)
		}
		fmt.Printf("Using %q as workspace name.\n", wsName)
		c.WSName = wsName
	}

	ws, err := c.workspaces.FetchWSSnapshot(ctx, c.WSName)
	if err != nil {
		return err
	}
	mountCtx, stopMount := context.WithCancel(ctx)
	defer stopMount()
	if err := c.mount(mountCtx); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, os.Args[0], "ws", "attach", "-s", "remote-wins", "--from-mount", c.WSName, c.MountPoint).CombinedOutput()
	if err != nil {
		_, _ = os.Stderr.Write(out)
		// Unmount before failing, so that no mount is left without the process that serves it.
		return unmountAfterFailure(stopMount, c.done, c.MountPoint, isMountPoint, fmt.Errorf("failed to attach workspace %s at %s: %w", c.WSName, c.MountPoint, err))
	}
	// Attaching runs abfs setup under sync.auto-setup.
	_, _ = os.Stdout.Write(out)

	client, err := binding.NewClient(c.MountPoint, entities.ABFSSocketFileName, c.Config.CacheDir, c.Config.LocalOnly, c.Config.FetchTimeout)
	if err != nil {
		return unmountAfterFailure(stopMount, c.done, c.MountPoint, isMountPoint, fmt.Errorf("failed to get cache client: %w", err))
	}
	defer func() {
		if err := client.Close(); err != nil {
			log.Printf("failed to close cache client: %v", err)
		}
	}()

	if err := binding.PrefetchDirs(c.Config.SyncPrefetchDirs, c.Cacheman, binding.ContentPrefetchPriority); err != nil {
		log.Printf("Content prefetch failed: %v", err)
	}

	for _, repo := range ws.Snapshot.GitRepos {
		objectsDir := path.Join(c.MountPoint, strings.Replace(repo.ObjectsDir, "{workdir}", repo.WorkDir, 1))
		packsDir := filepath.Join(objectsDir, "pack")

		if c.Config.GitPrefetchIndices {
			if err := binding.PrefetchDirs([]string{filepath.Join(packsDir, "*.idx")}, c.Cacheman, binding.ContentPrefetchPriority); err != nil {
				log.Printf("Pack index prefetch failed: %v", err)
			}
		}
	}

	if !c.Foreground {
		if c.daemon != nil {
			c.daemon.Switch()
		}
	}
	return <-c.done
}

// isMountPoint reports whether dir is the root of a mount.
func isMountPoint(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	mountPoint, err := cmd.MountPoint(abs)
	return err == nil && mountPoint == abs
}

var mountEndTimeout = 30 * time.Second

// unmountAfterFailure stops the mount, waits up to mountEndTimeout until it has ended and returns
// cause together with whether mountPoint is still mounted.
func unmountAfterFailure(stop func(), done <-chan error, mountPoint string, isMounted func(string) bool, cause error) error {
	stop()
	var mountErr error
	select {
	case mountErr = <-done:
	case <-time.After(mountEndTimeout):
		return fmt.Errorf("%w; %s is still unmounting after %s", cause, mountPoint, mountEndTimeout)
	}
	if isMounted(mountPoint) {
		if mountErr == nil {
			mountErr = errors.New("it is still mounted")
		}
		return fmt.Errorf("%w; failed to unmount %s: %w", cause, mountPoint, mountErr)
	}
	if mountErr != nil {
		log.Printf("Mount of %s ended with: %v", mountPoint, mountErr)
	}
	return fmt.Errorf("%w; unmounted %s again", cause, mountPoint)
}

func (c *mountCmd) mount(ctx context.Context) error {
	mountCmd := mount.NewCmd(c.RootCmd)
	fl := pflag.NewFlagSet("casfs-mount", pflag.ContinueOnError)
	mountCmd.RegisterFlags(fl)
	mountCmd.ConfigProvider = c.ConfigProvider
	mountCmd.StoreProvider = c.StoreProvider
	mountCmd.CachemanProvider = c.CachemanProvider
	serving := make(chan struct{})
	if !c.Foreground {
		mountCmd.AddCallback(func() error {
			close(serving)
			return nil
		})
	}
	args := []string{c.MountPoint}
	if err := fl.Parse(args); err != nil {
		return fmt.Errorf("failed to parse mount flags: %w", err)
	}

	go func() {
		defer close(c.done)
		c.done <- mountCmd.Exec(ctx, fl)
	}()

	select {
	case err := <-c.done:
		return err
	case <-serving:
		return nil
	}
}

func (c *mountCmd) startDaemon() error {
	args := os.Args[1:]
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), daemon.ABFSWSMountDaemonEnvVar+"=1")
	return daemon.ForkDaemon(cmd)
}

func (c *mountCmd) CloseDaemon() error {
	if c.daemon != nil {
		return c.daemon.Close()
	}
	return nil
}
