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

package sysreport

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

type sysReportCmd struct {
	*root.RootCmd
	reportDir string
}

func NewCmd(rootCmd *root.RootCmd) *sysReportCmd {
	return &sysReportCmd{RootCmd: rootCmd}
}

func (c *sysReportCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "sysreport",
		Usage:   "",
		Aliases: []string{},
		Desc:    `Generate a system report to assist with troubleshooting.`,
	}
}

func (c *sysReportCmd) captureCmd(cmd []string, outfile string) error {
	out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cmd: %v failed. stdout+stderr: %v, err: %v", cmd, strings.TrimSpace(string(out)), err)
	}

	return os.WriteFile(path.Join(c.reportDir, outfile), out, 0644)
}

func (c *sysReportCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *sysReportCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	d, err := os.MkdirTemp("", "")
	if err != nil {
		return err
	}
	c.reportDir = d

	fmt.Println("##################################################################")
	fmt.Println("This command executes sudo. You may be asked for sudo credentials.")
	fmt.Println("The report collection is best effort and you may see errors.")
	fmt.Println("##################################################################")
	fmt.Println()

	if err := c.captureCmd([]string{"abfs", "version", "--all"}, "abfs_version.txt"); err != nil {
		fmt.Printf("Error capturing abfs version: %v\n", err)
	}

	if err := c.captureCmd([]string{"uname", "-a"}, "uname.txt"); err != nil {
		fmt.Printf("Error capturing uname: %v\n", err)
	}

	if err := c.captureCmd([]string{"sudo", "dmesg", "-T"}, "dmesg.txt"); err != nil {
		fmt.Printf("Error capturing dmesg: %v\n", err)
	}

	if err := c.journalctlReport(); err != nil {
		fmt.Printf("Error capturing journalctl: %v\n", err)
	}

	if err := c.captureCmd([]string{"mount"}, "mount.txt"); err != nil {
		fmt.Printf("Error capturing mount info: %v\n", err)
	}

	if err := c.captureCmd([]string{"abfs", "locate"}, "locate.txt"); err != nil {
		fmt.Printf("Error capturing abfs locate: %v\n", err)
	}

	if err := c.captureCmd([]string{"abfs", "config"}, "config.txt"); err != nil {
		fmt.Printf("Error capturing abfs config: %v\n", err)
	}

	if err := c.captureCmd([]string{"abfs", "cacheman", "tail", "--all"}, "cacheman.txt"); err != nil {
		fmt.Printf("Error capturing abfs cacheman tail: %v\n", err)
	}

	if err := c.captureDaemonLog(); err != nil {
		fmt.Printf("Error capturing daemon log: %v\n", err)
	}

	if err := c.reportForMounts(); err != nil {
		fmt.Printf("Error capturing report for mounts: %v\n", err)
	}

	// Create the tarball one directory up to avoid mutating the report directory during the tar operation
	tarballName := fmt.Sprintf("abfs_report_%s.tar.gz", time.Now().Format("20060102150405"))
	tempTarballPath := path.Join(path.Dir(d), tarballName)
	if out, err := exec.Command("tar", "-czf", tempTarballPath, "-C", d, ".").CombinedOutput(); err != nil {
		return clierr.CLIErr("Could not tar the files in %s, err: %v\noutput: %v\n", d, err, string(out))
	}

	// Move it back into the report directory
	finalTarballPath := path.Join(d, "abfs_report.tar.gz")
	if err := os.Rename(tempTarballPath, finalTarballPath); err != nil {
		_ = os.Remove(tempTarballPath)
		return clierr.CLIErr("Could not move tarball to %s, err: %v\n", finalTarballPath, err)
	}

	msg := []string{
		"",
		"######################################################################",
		"Although sensitive data is unlikely to be present, consider",
		"inspecting the contents of the tarball and sanitizing the contents",
		"as desired, and re-create a tarball for sharing.",
		"######################################################################",
		"",
	}
	fmt.Println(strings.Join(msg, "\n"))
	fmt.Println("Report files are available at", path.Join(d, "abfs_report.tar.gz"))

	return nil
}

func (c *sysReportCmd) journalctlReport() error {
	abfsServiceUnits := []string{
		"-u", "abfs-server.service",
		"-u", "systemd-oomd",
	}

	var joined error

	suduJournalCtlKernel := []string{"sudo", "journalctl", "-t", "kernel"}
	if err := c.captureCmd(suduJournalCtlKernel, "sudo_journalctl_kernel.txt"); err != nil {
		joined = errors.Join(joined, err)
	}
	suduJournalCtlUnits := []string{"sudo", "journalctl"}
	suduJournalCtlUnits = append(suduJournalCtlUnits, abfsServiceUnits...)
	if err := c.captureCmd(suduJournalCtlUnits, "sudo_journalctl_units.txt"); err != nil {
		joined = errors.Join(joined, err)
	}

	userJournalCtlUnits := []string{"journalctl", "--user"}
	userJournalCtlUnits = append(userJournalCtlUnits, abfsServiceUnits...)
	if err := c.captureCmd(userJournalCtlUnits, "user_journalctl_units.txt"); err != nil {
		joined = errors.Join(joined, err)
	}

	return joined
}

func (c *sysReportCmd) copyFiles(mountPath string) error {
	cpFileList := []string{
		".repo/mount-details",
		".repo/manifests",
		".repo/abfs-manifests",
		"out/.ninja_log",
		"out/.wrap_log",
	}

	var joined error

	// Make the destination directories.
	for _, file := range cpFileList {
		srcFile := path.Join(mountPath, file)
		info, err := os.Stat(srcFile)
		if err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		if info.IsDir() {
			if err := os.MkdirAll(path.Join(c.reportDir, mountPath, file), 0755); err != nil {
				joined = errors.Join(joined, err)
			}
		} else {
			if err := os.MkdirAll(path.Dir(path.Join(c.reportDir, mountPath, file)), 0755); err != nil {
				joined = errors.Join(joined, err)
			}
		}
	}

	rsyncCmd := []string{
		"rsync", "-R", "-r", "--exclude=.git",
	}
	rsyncCmd = append(rsyncCmd, cpFileList...)
	rsyncCmd = append(rsyncCmd, path.Join(c.reportDir, mountPath))
	cmd := exec.Command(rsyncCmd[0], rsyncCmd[1:]...)
	cmd.Dir = mountPath
	if err := cmd.Run(); err != nil {
		joined = errors.Join(joined, err)
	}

	return joined
}

// Assumes file itself ends in a newline
func (c *sysReportCmd) lastLineInFile(path string) (result string, retErr error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}

	if info.Size() == 0 {
		return "", nil
	}

	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer entities.CloseErrCheck(file, &retErr)

	buffer := make([]byte, 1)
	// Start at 1st character from the end
	// We want to skip the last newline
	var i int64
	for i = 1; i <= info.Size(); i++ {
		offset := info.Size() - 1 - i

		_, err := file.ReadAt(buffer, offset)
		if err != nil && err != io.EOF {
			return "", err
		}

		if buffer[0] == '\n' {
			break
		}
	}

	// Start at first character after the second newline
	start := info.Size() - i
	// Read up to, but not including, the final newline
	buffer = make([]byte, i-1)
	_, err = file.ReadAt(buffer, start)
	if err != nil && err != io.EOF {
		return "", err
	}

	return string(buffer), nil
}

func (c *sysReportCmd) sampleLastBuildGoal(mountPath string) error {
	ninjaLine, err := c.lastLineInFile(path.Join(mountPath, "out", ".ninja_log"))
	if err != nil {
		return err
	}
	splits := strings.Fields(ninjaLine)
	if len(splits) < 7 {
		return fmt.Errorf("unexpected ninjaLine format: %q", ninjaLine)
	}

	evalReqRelPath := path.Join(mountPath, "eval_request_sample.txt")

	var joined error
	if err := c.captureCmd([]string{"abfs", "inspect", "-j", splits[3]}, evalReqRelPath); err != nil {
		joined = errors.Join(joined, err)
	} else {
		var decode struct {
			EnvVars string `json:"EnvVars"`
		}

		data, err := os.ReadFile(path.Join(c.reportDir, evalReqRelPath))
		if err != nil {
			joined = errors.Join(joined, err)
		} else {
			if err := json.Unmarshal(data, &decode); err != nil {
				joined = errors.Join(joined, err)
			} else {
				if err := c.captureCmd([]string{"abfs", "inspect", "-j", decode.EnvVars}, path.Join(mountPath, "envvars_sample.txt")); err != nil {
					joined = errors.Join(joined, err)
				}
			}
		}
	}

	if err := c.captureCmd([]string{"abfs", "inspect", "-j", splits[4]}, path.Join(mountPath, "eval_response_sample.txt")); err != nil {
		joined = errors.Join(joined, err)
	}

	return joined
}

func (c *sysReportCmd) reportForMount(mountPath string) error {
	if err := c.copyFiles(mountPath); err != nil {
		return err
	}

	if err := c.sampleLastBuildGoal(mountPath); err != nil {
		return err
	}

	return nil
}

func (c *sysReportCmd) reportForMounts() error {
	b, err := exec.Command("findmnt", "-t", "casfs", "-n", "-o", "TARGET").Output()
	if err != nil {
		return err
	}
	out := string(b)
	if out == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var joined error
	for _, line := range lines {
		if err := c.reportForMount(line); err != nil {
			joined = errors.Join(joined, fmt.Errorf("reportForMount(%v) failed: %v", line, err))
		}
	}

	return joined
}

func (c *sysReportCmd) captureDaemonLog() error {
	inst := c.ResolveInstance()
	if inst == nil || inst.Config == nil {
		return fmt.Errorf("could not resolve instance configuration")
	}
	logDir := inst.Config.LogDir
	if logDir == "" {
		return fmt.Errorf("log-dir is not set in config")
	}
	srcPath := path.Join(logDir, "abfs")
	dstPath := path.Join(c.reportDir, "abfs.log")

	srcFile, err := os.Open(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = srcFile.Close() }()

	dstFile, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer func() { _ = dstFile.Close() }()

	_, err = io.Copy(dstFile, srcFile)
	return err
}
