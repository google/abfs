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
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs"
	"github.com/google/abfs/binding"
	"github.com/google/abfs/cmd/abfs/doctor"
	"github.com/google/abfs/cmd/abfs/ws"
	"github.com/google/abfs/daemon"
	"github.com/google/abfs/filesystem"
	shellgit "github.com/google/abfs/git"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/metrics"
	"github.com/google/abfs/shared/user"
	"github.com/google/abfs/shared/version"
)

type daemonHolder interface {
	CloseDaemon() error
}

type mountCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	*cmd.StoreProvider
	*cmd.CachemanProvider
	*cmd.MountClientProvider
	isSnapshot             bool
	foreground             bool
	debug                  bool
	logHttp                bool
	logPathStrs            []string
	logPaths               []*regexp.Regexp
	revision               string
	manFile                string
	manFileHash            string
	manBranch              string
	manBranchFile          string
	localManDir            string
	metaRefPrefix          string
	changeIDs              []string
	port                   int
	mountPoint             string
	abfs                   *abfs.ABFS
	skipHealthCheck        bool
	force                  bool
	list                   bool
	ignoreMissingProjects  bool
	daemon                 *daemon.Daemon
	delegatedDaemonHolder  daemonHolder
	groups                 []string
	fallbackToGit          bool
	tmpWS                  bool
	ws                     string
	smartSync              bool
	smartTag               string
	manifestServerUsername string
	manifestServerPassword string
}

func NewCmd(rootCmd *root.RootCmd) *mountCmd {
	return &mountCmd{RootCmd: rootCmd}
}

func (c *mountCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name: "mount",
		Usage: `<one of the supported formats below>
	[--force] [--log-http] [--log-path <regex>] [-f <manifest-file>] [-b <branch>] [-p port] [-fs filesystem] [--skip-health-check] <mount-point>
	--list`,
		Aliases: []string{},
		Desc:    `Mount an ABFS on the given mount point directory.`,
	}
}

func (c *mountCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVar(&c.foreground, "foreground", false, "Run the command in foreground")
	fl.BoolVar(&c.debug, "debug", false, "Enable writing the debug messages for the file-system operations")
	fl.BoolVar(&c.logHttp, "log-http", false, "Log all the HTTP calls")
	fl.StringSliceVar(&c.logPathStrs, "log-path", nil, "Write all the logs that match the given pattern")
	fl.StringVarP(&c.manFile, "file", "f", "", "A pinned manifest file (like path to default.xml)")
	fl.StringVarP(&c.revision, "revision", "", "", "A commit revision")
	fl.StringVarP(&c.manFileHash, "manifest-hash", "h", "", "A pinned manifest file hash (blob hash of a manifest xml)")
	fl.StringVarP(&c.manBranch, "branch", "b", "", "Branch to find manifest from")
	fl.StringVarP(&c.manBranchFile, "branch-file", "", "default.xml", "Manifest file to use from the manifest branch")
	fl.IntVarP(&c.port, "port", "p", 0, "HTTP port to listen")
	fl.StringSliceVar(&c.changeIDs, "change", nil, "A gerrit change number to include in the checkout")
	fl.StringVarP(&c.metaRefPrefix, "ref-prefix", "r", "", "Prefix that will be used in the ABFS meta refs for builds and mounts (default is user/$USER)")
	fl.BoolVar(&c.skipHealthCheck, "skip-health-check", false, "Skip ABFS health check")
	fl.BoolVar(&c.force, "force", false, "Attempt to unmount an existing casfs mount")
	fl.BoolVar(&c.list, "list", false, "List active mounts")
	fl.BoolVar(&c.ignoreMissingProjects, "ignore-missing-projects", false, "Mount will skip projects from the manifest that can't be found (default false)")
	fl.StringSliceVarP(&c.groups, "groups", "g", nil, "Groups to filter. Same as repo init -g")
	fl.BoolVar(&c.fallbackToGit, "fallback-to-git", false, "Fallback to git operation directly if mount fails on a project.")
	fl.StringVar(&c.localManDir, "local-manifest-dir", "", "The dir storing local manifest ")
	fl.StringVar(&c.ws, "ws", "", "The name of the new workspace")
	fl.BoolVar(&c.tmpWS, "tmp-ws", false, "Use a temporary workspace")
	fl.BoolVarP(&c.smartSync, "smart-sync", "s", false, "Sync to a known good revision as specified by the manifest server")
	fl.StringVarP(&c.smartTag, "smart-tag", "t", "", "Sync to a tag as specified by the manifest server")
	fl.StringVar(&c.manifestServerUsername, "manifest-server-username", "", "Username to authenticate with the manifest server")
	fl.StringVar(&c.manifestServerPassword, "manifest-server-password", "", "Password to authenticate with the manifest server")
}

func (c *mountCmd) newMountDetails() (*binding.MountDetails, error) {
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

func (c *mountCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	if c.list {
		return list()
	}

	if !c.skipHealthCheck {
		if err := doctor.PreflightCheck(c.Instance); err != nil {
			return err
		}
	}

	c.mountPoint = args.EnsureSingleArg(fl)
	c.mountPoint = instance.AbsPath(c.mountPoint)

	if c.manFile != "" {
		absPath, err := filepath.Abs(c.manFile)
		if err != nil {
			return err
		}
		c.manFile = absPath
	}

	if c.StoreProvider != nil && c.Store != nil && c.manFileHash != "" {
		hash := entities.NewHash(c.manFileHash)
		content, err := c.Store.GetObject(ctx, hash)
		if err == nil {
			objType, _, err := entities.DecodeObject(content)
			if err == nil && objType == string(binding.ObjectTypeWSSnapshot) {
				if c.ws == "" && !c.tmpWS {
					return clierr.CLIErr("The manifest hash is a snapshot. Please specify --tmp-ws or --ws to mount a snapshot.")
				}
				c.isSnapshot = true
			}
		}
	}

	if c.Config.ForceWorkspaces || c.ws != "" || c.tmpWS {
		return c.workspaceMount(ctx)
	}

	if err := c.modeMatchingCheck(); err != nil {
		return err
	}

	if c.Config.LFS {
		if !(&shellgit.ShellGit{BinPath: "git"}).LFSEnabled() {
			return clierr.CLIErr("ABFS is configured to use LFS but LFS is not enabled")
		}
	}

	if c.foreground || daemon.IsDaemon() {
		return c.runForeground(ctx)
	}

	provider := metrics.GetProvider()

	mountTimer := provider.NewTimer(
		"abfs_mount_time_seconds",
		"Runtime for a mount operation",
		[]float64{2, 5, 10, 15, 20, 30, 40, 50, 60, 90, 120},
		nil,
	)
	err := c.startDaemon()
	mountTimer.Stop()

	return err
}

func (c *mountCmd) workspaceMount(ctx context.Context) error {
	mountRepoCmd := ws.NewMountRepoCmd(c.RootCmd, ws.MountRepoOpts{
		WSName:                 c.ws,
		TempWS:                 c.tmpWS,
		MountPoint:             c.mountPoint,
		Branch:                 c.manBranch,
		ManifestHash:           entities.NewHash(c.manFileHash),
		ManifestFile:           c.manFile,
		Groups:                 c.groups,
		Force:                  c.force,
		RefPrefix:              c.metaRefPrefix,
		SmartSync:              c.smartSync,
		SmartTag:               c.smartTag,
		ManifestServerUsername: c.manifestServerUsername,
		ManifestServerPassword: c.manifestServerPassword,
		IsSnapshot:             c.isSnapshot,
		FallbackToGit:          c.fallbackToGit,
	})
	mountRepoCmd.ConfigProvider = c.ConfigProvider
	mountRepoCmd.StoreProvider = c.StoreProvider
	mountRepoCmd.CachemanProvider = c.CachemanProvider
	mountRepoCmd.MountClientProvider = c.MountClientProvider
	c.delegatedDaemonHolder = mountRepoCmd
	return mountRepoCmd.Run(ctx)
}

func (c *mountCmd) startDaemon() error {
	args := os.Args[1:]
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), daemon.ABFSDaemonEnvVar+"=1")
	return daemon.ForkDaemon(cmd)
}

func manifestBytes(revision string) []byte {
	if revision == "" {
		return nil
	}

	return []byte(fmt.Sprintf(`<manifest>
	<project name="unnamed" revision="%s"></project>
</manifest>`, revision))
}

func (c *mountCmd) runForeground(ctx context.Context) (retErr error) {
	if daemon.IsDaemon() {
		d, err := daemon.SetupDaemon(filepath.Join(c.Config.LogDir, "abfs"))
		if err != nil {
			return err
		}
		c.daemon = d
	}

	c.logPaths = make([]*regexp.Regexp, len(c.logPathStrs))
	for i, p := range c.logPathStrs {
		c.logPaths[i] = regexp.MustCompile(p)
	}

	if err := bindcas.CheckMountPoint(c.mountPoint, c.force); err != nil {
		return err
	}

	go func() {
		lis, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", c.port))
		if err != nil {
			log.Fatalf("Could not bind to port %d: %v", c.port, err)
		}
		port := lis.Addr().(*net.TCPAddr).Port
		log.Printf("Starting a web-server on http://localhost:%d/", port)
		if err := http.Serve(lis, nil); err != nil {
			log.Fatalf("Error when starting or running http server: %v", err)
		}
	}()

	details, err := c.newMountDetails()
	if err != nil {
		return err
	}

	useRepo := c.Config.RepoDefault

	info := abfs.ABFSInfo{
		MountInfo: filesystem.MountInfo{
			CacheDir:       c.Config.CacheDir,
			LocalOnly:      c.Config.LocalOnly,
			FetchTimeout:   c.Config.FetchTimeout,
			PartialClone:   c.Config.PartialClone,
			RemoteReadOnly: c.Config.RemoteReadOnly,
		},
		MountPoint:   c.mountPoint,
		MountDetails: details,
		MountType:    c.Config.FSType,
		InitHash:     entities.EmptyDirHash,
		DebugConfig: &entities.FSConfig{
			Debug:               c.debug,
			LogHttp:             c.logHttp,
			LogPathsStr:         c.logPathStrs,
			LogPaths:            c.logPaths,
			IgnoreSoongEnvAvail: c.Config.IgnoreSoongEnvAvail,
		},
		ManifestScheme:      c.Config.ManifestScheme,
		ManifestServer:      c.Config.ManifestServer,
		ManifestFile:        c.manFile,
		ManifestProjectName: c.Config.ManifestProjectName,
		ManifestFileHash:    c.manFileHash,
		ManifestBranch:      c.manBranch,
		ManifestBranchFile:  c.manBranchFile,
		LocalManifestDir:    c.localManDir,
		ManifestBytes:       manifestBytes(c.revision),
		ChangeIDs:           c.changeIDs,
		LFS:                 c.Config.LFS,
		PrefetchDirs:        c.Config.SyncPrefetchDirs,
		PrefetchGitIndices:  c.Config.GitPrefetchIndices,
		PrefetchSymlinks:    c.Config.GitPrefetchSymlinks,
		GitRepoInit:         useRepo,
		Started: func() {
			if c.daemon != nil {
				c.daemon.Switch()
			}
		},
		IgnoreMissingProjects: c.ignoreMissingProjects,
		MountGroups:           c.groups,
		MountFallbackToGit:    c.fallbackToGit,
		AutoSetup:             c.Config.SyncAutoSetup,
	}

	fs, err := abfs.NewABFS(info)
	if err != nil {
		return err
	}
	c.abfs = fs

	return fs.Serve(ctx)
}

func (c *mountCmd) modeMatchingCheck() error {
	config := c.Config

	cachemanConfig, err := c.Cacheman.ReadConfig()
	if err != nil {
		return err
	}

	if cachemanConfig.LocalOnly != config.LocalOnly {
		return clierr.CLIErr("Mount and Cacheman config mismatch: local-only")
	}
	if cachemanConfig.RemoteReadOnly != config.RemoteReadOnly {
		return clierr.CLIErr("Mount and Cacheman config mismatch: remote-read-only")
	}

	return nil
}

func mountPointsForType(mntType string) ([]string, error) {
	cmd := exec.Command("findmnt", "-t", mntType, "-n", "-o", "TARGET")
	b, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	// strings.Split can return a slice of size 1 with [0] being empty
	s := strings.TrimSpace(string(b))
	if s == "" {
		return []string{}, err
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n"), nil
}

func list() error {
	casfsMounts, err := mountPointsForType("casfs")
	if err != nil {
		return err
	}

	if len(casfsMounts) > 0 {
		fmt.Println("casfs mounts:")
		for _, tgt := range casfsMounts {
			fmt.Printf("\t%s\n", tgt)
		}
	}
	return nil
}

func (c *mountCmd) CloseDaemon() error {
	var err error
	if c.daemon != nil {
		err = entities.FirstError(err, c.daemon.Close())
	}
	if c.delegatedDaemonHolder != nil {
		err = entities.FirstError(err, c.delegatedDaemonHolder.CloseDaemon())
	}
	return err
}
