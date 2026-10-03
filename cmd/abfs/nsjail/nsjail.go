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

package nsjail

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
)

// chrootPathsEnv names the environment variable holding a colon-separated list of
// candidate chroot paths, relative to the top of the source tree.
const chrootPathsEnv = "ABFS_NSJAIL_CHROOT_PATHS"

func knownChrootPaths() []string {
	var paths []string
	for _, p := range strings.Split(os.Getenv(chrootPathsEnv), ":") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

func rootFsBinds(chroot string) []string {
	mntPoints := []string{"bin", "sbin",
		"etc/alternatives", "etc/default", "etc/perl",
		"etc/ssl", "etc/xml",
		"lib", "lib32", "lib64", "libx32",
		"usr"}
	var mapping []string
	for _, mnt := range mntPoints {
		source := path.Join(chroot, mnt)
		dst := path.Join("/", mnt)
		if _, err := os.Stat(source); err == nil {
			mapping = append(mapping, []string{"-B", source + ":" + dst}...)
		}
	}
	return mapping
}

// ChrootPath searches the candidate chroot paths configured via ABFS_NSJAIL_CHROOT_PATHS
// and returns the first one that exists under rootDir.
func ChrootPath(rootDir string) (string, error) {
	candidates := knownChrootPaths()
	if len(candidates) == 0 {
		return "", fmt.Errorf("no chroot paths configured; set %s to a colon-separated list of chroot paths relative to the source tree", chrootPathsEnv)
	}
	for _, chroot := range candidates {
		if path.IsAbs(chroot) {
			return "", fmt.Errorf("%s entries must be relative to the source tree, got %q", chrootPathsEnv, chroot)
		}
		if _, err := os.Stat(path.Join(rootDir, chroot)); err == nil {
			return chroot, nil
		}
	}
	return "", fmt.Errorf("couldn't find chroot in the following path: %v", candidates)
}

func (c *nsjailCmd) runNSJail(bindMounts []string, envVars []string, command ...string) (err error) {

	chroot, err := ChrootPath(c.rootDir)
	if err != nil {
		return err
	}
	chroot = path.Join(c.rootDir, chroot)

	// Add explicit mount to .ctrl under /src as /src can be configured as read only
	// during build.
	dirMappings := append(rootFsBinds(chroot),
		[]string{
			"-B", c.rootDir + ":/src",
			"-B", path.Join(c.rootDir, ".ctrl") + ":/src/.ctrl",
			"-B", c.Config.CacheDir + ":" + expectedCASCachePath,
			"-B", path.Dir(c.Config.CacheDir),
		}...)
	for _, bindMount := range bindMounts {
		// Allow bindmounts to be specified with relative paths.
		if strings.HasPrefix(bindMount, "/") {
			dirMappings = append(dirMappings, "-B", bindMount)
		} else {
			dirMappings = append(dirMappings, "-B", path.Join(c.rootDir, bindMount))
		}
	}

	return RunNSJail(c.nsJailBin, c.nsJailCfg, dirMappings, envVars, command...)
}

func nsJailArgs(nsJailCfg string, extraArgs []string, command ...string) []string {
	args := []string{
		"--config", nsJailCfg,
		"-H", "android-build", // hostname
		"--env", "USER=nobody",
	}

	args = append(args, extraArgs...)
	args = append(args, "--")
	args = append(args, command...)
	return args
}

func NSJailCommand(nsJailBin, nsJailCfg string, extraArgs []string, command ...string) *exec.Cmd {
	args := nsJailArgs(nsJailCfg, extraArgs, command...)
	return exec.Command(nsJailBin, args...)
}

func NSJailCommandContext(ctx context.Context, nsJailBin, nsJailCfg string, extraArgs []string, command ...string) *exec.Cmd {
	args := nsJailArgs(nsJailCfg, extraArgs, command...)
	return exec.CommandContext(ctx, nsJailBin, args...)
}

func RunNSJail(nsJailBin, nsJailCfg string, opts []string, envVars []string, command ...string) (err error) {
	env := make([]string, 0, 2*len(envVars))
	for _, kv := range envVars {
		env = append(env, "--env", kv)
	}

	cmd := NSJailCommand(nsJailBin, nsJailCfg, append(opts, env...), command...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// RunNSJailCapture captures stdout but directs stderr to os.Stderr
func RunNSJailCapture(nsJailBin, nsJailCfg string, opts []string, command ...string) (stdout []byte, err error) {
	cmd := NSJailCommand(nsJailBin, nsJailCfg, opts, command...)
	stdOut := new(bytes.Buffer)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdOut
	cmd.Stderr = os.Stderr

	err = cmd.Run()
	return stdOut.Bytes(), err
}

// RunNSJailCaptureAll captures stderr and stdout
func RunNSJailCaptureAll(ctx context.Context, nsJailBin, nsJailCfg string, opts []string, command ...string) (stdout []byte, err error) {
	cmd := NSJailCommandContext(ctx, nsJailBin, nsJailCfg, opts, command...)

	return cmd.CombinedOutput()
}
