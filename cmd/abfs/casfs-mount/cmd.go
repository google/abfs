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

package mount

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/daemon"
	"github.com/google/abfs/filesystem"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/user"
	"github.com/google/abfs/shared/version"
)

const CASFSDaemonEnvVar = "CASFS_MOUNT_DAEMON"

type casfsMountCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	*cmd.StoreProvider
	*cmd.CachemanProvider

	debug         bool
	port          int
	socket        string
	cpuProf       string
	useDaemon     bool
	checkMount    string
	mountPoint    string
	metaRefPrefix string
	daemon        *daemon.Daemon
	callbacks     []func() error
}

func (c *casfsMountCmd) AddCallback(f func() error) {
	c.callbacks = append(c.callbacks, f)
}

func NewCmd(rootCmd *root.RootCmd) *casfsMountCmd {
	return &casfsMountCmd{RootCmd: rootCmd}
}

func (c *casfsMountCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "casfs-mount",
		Usage:   "[options] <mount-point>",
		Aliases: []string{},
		Desc:    `Mount a CASFS on the given mount point directory.`,
	}
}

func (c *casfsMountCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.debug, "debug", "d", false, "Enable debug logs")
	fl.IntVarP(&c.port, "port", "p", 0, "HTTP port to listen")
	fl.StringVarP(&c.socket, "socket", "s", entities.ABFSSocketFileName, "The relative path for unix domain socket to listen")
	fl.StringVar(&c.cpuProf, "cpu-pprof", "", "Enable cpu pprof, writing data to this file")
	fl.BoolVar(&c.useDaemon, "daemon", false, "Run the server in a daemon more (puts the server in background on a successful mount)")
	args.OptChoice(fl, &c.checkMount, "check-mount", "", "check", []string{"ignore", "check", "force"}, "Check the mount point before mounting, 'force' will force unmount any ABFS mounted file systems.")
	fl.StringVarP(&c.metaRefPrefix, "ref-prefix", "r", "", "Prefix that will be used in the ABFS meta refs for builds and mounts (default is user/$USER)")
	args.SetPath(fl, "cpu-pprof")

}

func (c *casfsMountCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	mntPt, err := filepath.Abs(args.EnsureSingleArg(fl))
	if err != nil {
		return clierr.CLIErr("Failed to get absolute path for mount point: %v", err)
	}
	c.mountPoint = mntPt

	if !c.useDaemon || isDaemon() {
		return c.runForeground(ctx)
	}

	return c.startDaemon()
}

func isDaemon() bool {
	return os.Getenv(CASFSDaemonEnvVar) == "1"
}

func (c *casfsMountCmd) startDaemon() error {
	args := os.Args[1:]
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), CASFSDaemonEnvVar+"=1")
	return daemon.ForkDaemon(cmd)
}

func (c *casfsMountCmd) runForeground(ctx context.Context) (retErr error) {
	if isDaemon() {
		d, err := daemon.SetupDaemon(filepath.Join(c.Config.LogDir, "abfs"))
		if err != nil {
			return err
		}
		c.daemon = d
	}

	if c.checkMount != "ignore" {
		if err := bindcas.CheckMountPoint(c.mountPoint, c.checkMount == "force"); err != nil {
			return err
		}
	}

	if c.port != 0 {
		go func() {
			if err := http.ListenAndServe(fmt.Sprintf(":%d", c.port), nil); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("Error when starting or running http server: %v", err)
			}
		}()
	}

	details, err := c.newMountDetails()
	if err != nil {
		return err
	}

	info := filesystem.MountInfo{
		CacheDir:       c.Config.CacheDir,
		LocalOnly:      c.Config.LocalOnly,
		FetchTimeout:   c.Config.FetchTimeout,
		PartialClone:   c.Config.PartialClone,
		RemoteReadOnly: c.Config.RemoteReadOnly,
	}

	casfsMntOpts := &daemon.CASFSMountOpts{
		Type:            c.Config.FSType,
		Info:            info,
		MountPoint:      c.mountPoint,
		Socket:          c.socket,
		MountDetails:    details,
		ManifestServer:  c.Config.ManifestServer,
		MountedCallback: c.onMountFinished,
		Store:           c.Store,
		Cacheman:        c.Cacheman,
	}
	d := daemon.NewCASFS(casfsMntOpts)

	finished := make(chan struct{})
	defer close(finished)

	d.SetInitConfig(&entities.FSConfig{
		Debug:               c.debug,
		LogHttp:             false,
		IgnoreSoongEnvAvail: c.Config.IgnoreSoongEnvAvail,
	})

	if c.cpuProf != "" {
		f, err := os.Create(c.cpuProf)
		if err != nil {
			return clierr.CLIErr("Failed to open CPU profile file: %v", err)
		}
		defer func() { retErr = entities.FirstError(retErr, f.Close()) }()
		if err := pprof.StartCPUProfile(f); err != nil {
			return clierr.CLIErr("Failed to start CPU profiling: %v", err)
		}
		defer pprof.StopCPUProfile()
	}

	return d.Serve(ctx)
}

func (c *casfsMountCmd) onMountFinished() {
	for _, cb := range c.callbacks {
		if err := cb(); err != nil {
			log.Printf("Error in callback: %v", err)
		}
	}
	if c.useDaemon {
		c.daemon.Switch()
	}
}

func (c *casfsMountCmd) newMountDetails() (*binding.MountDetails, error) {
	currUser, err := user.CurrentUser()
	if err != nil {
		return nil, err
	}

	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}

	refPrefix := c.metaRefPrefix
	if refPrefix == "" {
		refPrefix = "user/" + currUser.Username
	}

	refPrefix = path.Clean(refPrefix)
	if refPrefix == "" || refPrefix == "." || refPrefix == ".." || strings.HasPrefix(refPrefix, "../") {
		return nil, fmt.Errorf("invalid ref-prefix: %q", refPrefix)
	}

	now := time.Now()
	return &binding.MountDetails{
		Version:        binding.CurrentMountDetailsVersion,
		PID:            os.Getpid(),
		StartTime:      now,
		StartTimestamp: now.UnixNano(),
		MetaRefPrefix:  refPrefix,
		CmdArgs:        os.Args,
		UserName:       currUser.Username,
		HostName:       hostname,
		ABFSVersion:    version.Version(),
		Config:         c.Config.Clone(),
	}, nil
}

func (c *casfsMountCmd) CloseDaemon() error {
	if c.daemon != nil {
		return c.daemon.Close()
	}
	return nil
}
