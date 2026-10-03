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

package unmount

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/mergestat/timediff"
	"github.com/spf13/pflag"

	"github.com/google/abfs"
	"github.com/google/abfs/binding"
	"github.com/google/abfs/cmd/abfs/doctor"
	"github.com/google/abfs/filesystem/kmod"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/remote/grpc/client"
	"github.com/google/abfs/shared/user"
	"github.com/google/abfs/workspaces"
)

const (
	killABFSDaemonTimeout = 30 * time.Second
	unmountTimeout        = 30 * time.Second
)

var (
	errMountPoint = errors.New("not a mount point")
)

type unmountCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	*cmd.StoreProvider
	*cmd.CachemanProvider
	fsMode          instance.CASFSType
	mountDetails    *binding.MountDetails
	unmountDetails  *binding.UnmountDetails
	removeWS        bool
	force           bool
	kill            bool
	outputHash      bool
	noMountDetails  bool
	all             bool
	skipHealthCheck bool
}

func NewCmd(rootCmd *root.RootCmd) *unmountCmd {
	return &unmountCmd{RootCmd: rootCmd}
}

func (c *unmountCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name: "unmount",
		Usage: `<one of the supported formats below>
	[-k] [-h] [-i] --all
	[-k] [-h] [-i] <mount-point>`,
		Aliases: []string{"umount"},
		Desc:    `Stop the abfs serving the given mount point and unmount it.`,
	}
}

func (c *unmountCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVar(&c.removeWS, "rm", false, "Remove the workspace after the unmount")
	fl.BoolVarP(&c.force, "force", "f", false, "Force remove workspace (do not confirm the removal)")
	fl.BoolVarP(&c.kill, "kill", "k", false, "Kill processes that keep the mount busy")
	fl.BoolVarP(&c.outputHash, "hash", "h", false, "Output the hash of the unmount object to stdout")
	fl.BoolVarP(&c.noMountDetails, "no-mount-details", "i", false, "Do not read mount-details and do not write unmount-details")
	fl.BoolVar(&c.all, "all", false, "Unmount all casfs and fuse mounts")
	fl.BoolVar(&c.skipHealthCheck, "skip-health-check", false, "Skip health check before unmounting the workspace")
}

func resolveMountPoint(mountPoint string) (string, error) {
	abs, err := filepath.Abs(mountPoint)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path %q: %w", mountPoint, err)
	}
	eval, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs, nil
	}
	return eval, nil
}
func (c *unmountCmd) checkCallingDirectory(mountPoint string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil // Best effort
	}

	evalCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		evalCWD = cwd
	}

	rel, err := filepath.Rel(mountPoint, evalCWD)
	if err == nil && !strings.HasPrefix(rel, "..") {
		return clierr.CLIErr("Cannot unmount %q: Your current working directory (%s) is inside the mount point.\nPlease `cd` out of the directory and try again.", mountPoint, cwd)
	}
	return nil
}
func (c *unmountCmd) singleUnmount(ctx context.Context, mountPoint string) error {
	resolvedMP, err := resolveMountPoint(mountPoint)
	if err != nil {
		return err
	}
	mountPoint = resolvedMP
	wss := workspaces.NewWorkspaces(c.Store, c.Cacheman)

	wsStatus, err := wss.StatusFromPath(mountPoint)
	wsStatusNotExists := entities.IsNotExist(err)
	if err != nil && !wsStatusNotExists {
		return err
	}
	if c.removeWS && err != nil {
		fmt.Printf("No workspaces found mounted at %q\n", mountPoint)
	}

	// Safety check: verify workspace is in sync with remote before unmounting
	if !wsStatusNotExists && wsStatus.LastSyncError != "" && !c.force {
		fmt.Fprintf(os.Stderr, "\n⚠️  WARNING: Workspace %q has unpushed changes that failed to sync to remote:\n", wsStatus.Name)
		fmt.Fprintf(os.Stderr, "   Error: %s\n", wsStatus.LastSyncError)
		if !wsStatus.LastUpdate.IsZero() {
			fmt.Fprintf(os.Stderr, "   Last successful remote push: %s\n", timediff.TimeDiff(wsStatus.LastUpdate))
		} else {
			fmt.Fprintf(os.Stderr, "   Last successful remote push: never (workspace has never been synced to remote)\n")
		}
		fmt.Fprintf(os.Stderr, "   Unmounting now means remote servers will not have your latest changes.\n\n")

		msg := fmt.Sprintf("Do you want to proceed with unmounting %q despite unsynced changes? [y/N]: ", wsStatus.Name)
		if !root.AskYesNo(ctx, msg) {
			return clierr.CLIErr("Unmount aborted. Fix remote connectivity and wait for sync, or pass --force to unmount anyway.")
		}
	}

	if err := c.run(ctx, mountPoint); err != nil {
		return err
	}

	if c.removeWS {
		if !c.force {
			if !root.AskYesNo(ctx, fmt.Sprintf("Unmounted successfully. Do you want to remove workspace %q (%s)? [y/N]: ", wsStatus.Name, wsStatus.LastSnapshot)) {
				return clierr.CLIErr("User canceled workspace removal. You can still remove it with: `abfs ws remove %s`", wsStatus.Name)
			}
		}

		if err := wss.Remove(wsStatus.Name, entities.ZeroHash); err != nil {
			return fmt.Errorf("failed to remove workspace %s: %v", wsStatus.Name, err)
		}
	} else if !wsStatusNotExists {
		cmd.Printf("Unmounted successfully.\nThe workspace %q still exists and can be remounted with `abfs remount %s --ws %s`\n", wsStatus.Name, mountPoint, wsStatus.Name)
	}

	if c.unmountDetails != nil && !c.noMountDetails {
		if err := c.saveUnmountDetails(ctx); err != nil {
			return err
		}
		if c.outputHash {
			// output the unmountdetails hash for programmatic interaction with this command
			fmt.Print(c.unmountDetails.Hash)
		}
	}

	fmt.Println("Done.")
	return nil
}

func (c *unmountCmd) unmountAll(ctx context.Context) error {
	casfsMounts, err := mountPointsForType(ctx, "casfs")
	if err != nil {
		return err
	}

	fuseMounts, err := mountPointsForType(ctx, "fuse.abfs")
	if err != nil {
		return err
	}

	for _, mntPoint := range casfsMounts {
		if err := c.singleUnmount(ctx, mntPoint); err != nil {
			return err
		}
	}

	for _, mntPoint := range fuseMounts {
		if err := c.singleUnmount(ctx, mntPoint); err != nil {
			return err
		}
	}

	return nil
}

func (c *unmountCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	if !c.force && !c.skipHealthCheck {
		skipChecks := doctor.CheckCASFSCompatibility
		if err := doctor.PreflightCheck(c.Instance, skipChecks); err != nil {
			return err
		}
	}

	if c.all {
		return c.unmountAll(ctx)
	}

	mountPoint := args.EnsureSingleArg(fl)
	return c.singleUnmount(ctx, mountPoint)
}

func (c *unmountCmd) checkMountPoint(mountPoint string) error {

	mnt, err := bindcas.MountInfoForPath(mountPoint)
	if err != nil {
		return err
	}
	if mnt.Mount != mountPoint {
		return clierr.CLIErr("%q is not a mount point (its mount-point is %q).", mountPoint, mnt.Mount)
	}
	switch mnt.Type {
	case "casfs":
		c.fsMode = instance.CASFSTypeKmod
		return nil
	default:
		return clierr.CLIErr("Cannot unmount non-abfs file-system (%s). Try: `sudo umount %s`", mnt.Type, mountPoint)
	}
}

func (c *unmountCmd) run(ctx context.Context, mountPoint string) error {

	if err := c.checkCallingDirectory(mountPoint); err != nil {
		return err
	}

	if err := c.checkMountPoint(mountPoint); err != nil {
		return err
	}

	if !c.noMountDetails {
		if err := c.readMountDetails(mountPoint); err != nil {
			log.Printf("cannot read mount-details: %s", err)
		}
	}
	if c.mountDetails != nil {
		now := time.Now()
		c.unmountDetails = &binding.UnmountDetails{
			Version:         binding.CurrentUnmountDetailsVersion,
			UnmountTime:     now,
			MountDetails:    c.mountDetails.Hash,
			MountedDuration: now.Sub(c.mountDetails.StartTime).Seconds(),
		}
	}
	pids, err := c.processesUsingFS(ctx, mountPoint)
	if err != nil {
		return err
	}

	pids, abfsExists := c.filterPIDs(pids)
	if c.terminateEditMonitors(pids) {
		pids, err = c.processesUsingFS(ctx, mountPoint)
		if err != nil {
			return err
		}
		pids, abfsExists = c.filterPIDs(pids)
	}
	if len(pids) > 0 && !c.kill {
		if abfsExists {
			fmt.Printf("ABFS daemon is running (%d)...\n", c.mountDetails.PID)
		}
		fmt.Printf("Mount point %q is busy, List of processes:\n", mountPoint)
		for _, p := range pids {
			cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", p))
			if err != nil {
				continue
			}

			cmdline = bytes.Trim(cmdline, "\x00")
			escaped := shellescape.QuoteCommand(strings.Split(string(cmdline), "\x00"))
			fmt.Printf("  %-10d %s\n", p, escaped)
		}
		fmt.Println("Try again with the `--kill` flag if you want to kill these processes.")
		return clierr.CLIErrNoMessage(1)
	}
	fmt.Printf("Unmounting %s...\n", mountPoint)
	// Try to kill processes with SIGINT
	for _, pid := range pids {
		if proc, err := os.FindProcess(pid); err == nil { // No errors
			if err := proc.Signal(os.Interrupt); err != nil {
				log.Printf("error trying to kill process %d: %v", pid, err)
			}
		}
	}
	if abfsExists {
		if proc, err := os.FindProcess(c.mountDetails.PID); err == nil { // No errors
			if err := proc.Signal(os.Interrupt); err != nil {
				log.Printf("error trying to kill abfs daemon (%s), but continuing to unmount...", err)
			}

			startTime := time.Now()
			if err := waitForProcExit(proc, killABFSDaemonTimeout); err != nil {
				log.Printf("failed to wait for abfs daemon to die: %s", err)
			} else if c.unmountDetails != nil {
				c.unmountDetails.ABFSKillDuration = float64(time.Since(startTime).Seconds())
			}
		}
	}
	pids, err = c.processesUsingFS(ctx, mountPoint)
	if err != nil {
		if errors.Is(err, errMountPoint) {
			// Killing abfs did the unmount
			return nil
		}
		return err
	}
	if len(pids) > 0 && !c.kill {
		return fmt.Errorf("file-system is still busy: %v", pids)
	}
	// `kill -9` remaining pids
	for _, pid := range pids {
		if proc, err := os.FindProcess(pid); err == nil { // No errors
			if err := proc.Signal(os.Kill); err != nil {
				log.Printf("Failed to kill process %d: %s", proc.Pid, err)
			}
		}
	}
	return c.unmount(ctx, mountPoint, unmountTimeout)
}

func waitForProcExit(proc *os.Process, timeout time.Duration) error {
	startTime := time.Now()
	for time.Since(startTime) < timeout {
		time.Sleep(100 * time.Millisecond)
		// sending signal 0 only checks the process existence
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return nil
		}
	}
	return fmt.Errorf("process did not die in %s", timeout)
}

func (c *unmountCmd) unmount(ctx context.Context, mountPoint string, timeout time.Duration) error {
	startTime := time.Now()
	for time.Since(startTime) < timeout {
		unmountTime := time.Now()
		busy, err := c.tryUnmount(ctx, mountPoint)
		if err != nil {
			return err
		}
		if !busy {
			if c.unmountDetails != nil {
				c.unmountDetails.UnmountDuration = float64(time.Since(unmountTime).Seconds())
				c.unmountDetails.UnmountBusy = float64(unmountTime.Sub(startTime).Seconds())
			}
			return nil
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("file-system was busy for %s", timeout)
}

func (c *unmountCmd) tryUnmount(ctx context.Context, mountPoint string) (busy bool, err error) {
	return c.unmountCASFS(ctx, mountPoint)
}

func (c *unmountCmd) unmountCASFS(ctx context.Context, mountPoint string) (busy bool, err error) {
	bin, err := kmod.CasfsMounterPath()
	if err != nil {
		return false, err
	}
	cmd := exec.CommandContext(ctx, bin, "-u", mountPoint)
	buf, err := cmd.CombinedOutput()
	if err == nil {
		return false, nil
	}
	if strings.Contains(string(buf), "device or resource busy") {
		return true, nil
	}
	return false, fmt.Errorf("failed to unmount: %s\n%v", buf, err)
}

func (c *unmountCmd) filterPIDs(pids []int) (filterPIDs []int, abfsProcExists bool) {
	for i, p := range pids {
		if c.mountDetails != nil && c.mountDetails.PID == p {
			return append(pids[:i], pids[i+1:]...), true
		}
	}
	return pids, false
}

// Attempts to terminate any edit_monitor processes running in the mount point.
// Returns true if edit_monitors processes were found and signaled.
func (c *unmountCmd) terminateEditMonitors(pids []int) bool {
	var killed []*os.Process
	for _, pid := range pids {
		if isEditMonitorProcess(pid) {
			if proc, err := os.FindProcess(pid); err == nil {
				if err := proc.Signal(syscall.SIGTERM); err == nil {
					killed = append(killed, proc)
				}
			}
		}
	}
	if len(killed) == 0 {
		return false
	}
	for _, proc := range killed {
		_ = waitForProcExit(proc, 2*time.Second)
	}
	return true
}

func isEditMonitorProcess(pid int) bool {
	if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
		base := filepath.Base(exe)
		if base == "edit_monitor" {
			return true
		}
	}
	return false
}

func (c *unmountCmd) readMountDetails(mountPoint string) (retErr error) {
	f, err := os.Open(path.Join(mountPoint, abfs.MountDetailsFileName))
	if err != nil {
		return err
	}
	defer func() { retErr = entities.FirstError(retErr, f.Close()) }()

	var details struct {
		*binding.MountDetails
		Hash string
	}

	if err := json.NewDecoder(f).Decode(&details); err != nil {
		return err
	}
	d := details.MountDetails
	d.Hash = entities.NewHash(details.Hash)
	c.mountDetails = d
	return nil
}

func (c *unmountCmd) processesUsingFS(ctx context.Context, mountPoint string) ([]int, error) {
	cmd := exec.CommandContext(ctx, "fuser", "-M", "-m", mountPoint)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	// fuser exits non-zero if it cannot find any process keeping the mount-point busy
	err := cmd.Run()

	if strings.Contains(errBuf.String(), "is not a mountpoint") {
		return nil, errMountPoint
	}
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			return nil, err
		}
	}

	pids := make([]int, 0)
	for p := range strings.FieldsSeq(outBuf.String()) {
		pid, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("cannot part pid: %q", p)
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

func (c *unmountCmd) saveUnmountDetails(ctx context.Context) error {
	details := c.unmountDetails
	if err := details.Encode(); err != nil {
		return err
	}

	conf := c.mountDetails.Config

	if err := c.Store.PutObject(ctx, details.Hash, details.Encoded); err != nil {
		return fmt.Errorf("failed to put UnmountDetails with hash %s: %w", details.Hash, err)
	}

	if conf.LocalOnly {
		return nil
	}

	servers := conf.EffectiveRemoteServers()
	cli, err := client.NewABFSClient(&client.ABFSClientOpts{
		Servers:  servers,
		AuthInfo: conf.GetAuthInfo(),
	})
	if err != nil {
		return fmt.Errorf("error building ABFS client for servers %s: %w", servers, err)
	}
	defer func() {
		if err := cli.Close(); err != nil {
			log.Printf("Failed to close the client: %s", err)
		}
	}()

	ref := fmt.Sprintf("refs/abfs/unmount/%s/%d", c.mountDetails.MetaRefPrefix, c.mountDetails.StartTimestamp)
	refs := []entities.RefHash{{Ref: ref, Hash: details.Hash}}
	if err := c.Store.PutRefs(conf.ManifestServer, entities.ABFSMetaProject, refs); err != nil {
		return fmt.Errorf("failed to update %s to %s: %s", ref, details.Hash, err)
	}

	return nil
}

// mountPointsForType make best effort to find mount points owned by
// the current user.
func mountPointsForType(ctx context.Context, mntType string) ([]string, error) {
	currUser, err := user.CurrentUser()
	if err != nil {
		return nil, err
	}
	currUID, err := strconv.Atoi(currUser.Uid)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "findmnt", "-t", mntType, "-n", "-o", "TARGET")
	b, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	// strings.Split can return a slice of size 1 with [0] being empty
	s := strings.TrimSpace(string(b))
	if s == "" {
		return []string{}, err
	}
	all := strings.Split(strings.TrimSpace(string(b)), "\n")
	var owned []string
	for _, mnt := range all {
		fi, err := os.Stat(mnt)
		if err != nil {
			continue
		}

		if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
			if uint32(currUID) == stat.Uid {
				owned = append(owned, mnt)
			}
		}
	}

	return owned, nil
}
