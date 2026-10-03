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

package purge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/iafan/cwalk"
	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
)

type purgeCmd struct {
	*root.RootCmd
	force bool
}

var abfsBinaries = map[string]bool{
	"abfs":                     true,
	"casfs-mounter":            true,
	"casfuse-mounter":          true,
	"tap-no-cgo":               true,
	"abfsbox":                  true,
	"abfsbox-no-cgo":           true,
	"ninjago":                  true,
	"ninjago-no-cgo":           true,
	"abfs-wrap-no-cgo":         true,
	"abfs-cmd-map-no-cgo":      true,
	"abfs-bazel-daemon":        true,
	"abfs-bazel-daemon-no-cgo": true,
	"rbetap":                   true,
	"rbetap-no-cgo":            true,
	"cacheman":                 true,
	"abfs-resources":           true,
	"abfs-rbe":                 true,
}

func NewCmd(rootCmd *root.RootCmd) *purgeCmd {
	return &purgeCmd{RootCmd: rootCmd}
}

func (c *purgeCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "purge",
		Usage:   "[-f]",
		Aliases: []string{},
		Desc:    "Make best effort to purge all ABFS processes, systemd services, binaries, configurations, caches, and logs.",
	}
}

func (c *purgeCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.force, "force", "f", false, "Force without confirmation")
}

func (c *purgeCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	if !c.force {
		reader := bufio.NewReader(os.Stdin)
		log.Printf("Confirm purging all ABFS state (Y/n)")
		answer, _ := reader.ReadString('\n')
		if strings.ToUpper(strings.TrimSpace(answer)) != "Y" {
			log.Printf("Not confirmed with Y, purge skipped")
			return nil
		}
	}

	log.Println("Making best effort attempt to purge all ABFS state...")

	log.Println("Trying to kill any active mounts...")
	unmountAll()

	log.Println("Stopping and removing ABFS systemd services...")
	removeSystemdServices()

	log.Println("Stopping running ABFS processes...")
	killABFSProcesses()

	log.Println("Removing ABFS binaries from user PATH...")
	removeBinaries()

	log.Println("Clearing abfs bash completions...")
	removeBashCompletions()

	log.Println("Removing CASFS kernel module...")
	removeCASFS()

	log.Println("Removing ABFS configurations, caches, and logs...")
	if err := removeBaseDirConcurrently(ctx); err != nil {
		log.Printf("Warning: failed to remove ABFS base dir: %v", err)
	}

	log.Println("ABFS purge complete!")
	return nil
}

func killABFSProcesses() {
	myPid := os.Getpid()
	myPpid := os.Getppid()
	myUid := os.Getuid()

	entries, err := os.ReadDir("/proc")
	if err != nil {
		log.Printf("Warning: failed to read /proc: %v", err)
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		// Don't kill self.
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if pid == myPid || pid == myPpid {
			continue
		}

		// Only look at user processes.
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			if int(stat.Uid) != myUid {
				continue
			}
		}

		// Kill any abfs related processes.
		exePath, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if err != nil {
			continue
		}

		exeName := filepath.Base(exePath)
		if abfsBinaries[exeName] {
			proc, err := os.FindProcess(pid)
			if err == nil {
				if err := proc.Kill(); err != nil {
					log.Printf("Warning: failed to kill process %d (%s): %v", pid, exeName, err)
				}
			}
		}
	}
}

func execCmd(command string, args []string, errorMsg string) {
	cmd := exec.Command(command, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("Warning: %s: %v", errorMsg, err)
	}
}

func removeSystemdServices() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("Warning: failed to get user home dir: %v", err)
		return
	}

	systemdDir := filepath.Join(home, ".config", "systemd", "user")
	patterns := []string{
		filepath.Join(systemdDir, "cacheman@*.service"),
	}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			log.Printf("Warning: glob failed for pattern %s: %v", pattern, err)
			continue
		}
		for _, match := range matches {
			unit := filepath.Base(match)
			execCmd("systemctl", []string{"--user", "stop", unit}, fmt.Sprintf("failed to stop systemd service %s", unit))
			execCmd("systemctl", []string{"--user", "disable", unit}, fmt.Sprintf("failed to disable systemd service %s", unit))
			if err := os.Remove(match); err != nil && !errors.Is(err, fs.ErrNotExist) {
				log.Printf("Warning: failed to remove service file %s: %v", match, err)
			}
		}
	}

	execCmd("systemctl", []string{"--user", "daemon-reload"}, "systemctl daemon-reload failed")
}

func unmountAll() {
	execCmd("abfs", []string{"unmount", "-k", "--all"}, "failed to unmount all abfs mounts")
}

func getKernelRelease() (string, error) {
	var uts syscall.Utsname
	if err := syscall.Uname(&uts); err != nil {
		return "", err
	}
	var buf []byte
	for _, b := range uts.Release {
		if b == 0 {
			break
		}
		buf = append(buf, byte(b))
	}
	return string(buf), nil
}

func removeBinaries() {
	pathEnv := os.Getenv("PATH")
	pathDirs := filepath.SplitList(pathEnv)

	for b := range abfsBinaries {
		for _, dir := range pathDirs {
			if dir == "" {
				continue
			}
			target := filepath.Join(dir, b)
			if stat, err := os.Stat(target); err == nil && !stat.IsDir() {
				if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
					execCmd("sudo", []string{"rm", "-f", target}, fmt.Sprintf("failed to remove binary %s", target))
				}
			}
		}
	}
}

func removeBashCompletions() {
	target := "/etc/bash_completion.d/abfs"
	if stat, err := os.Stat(target); err == nil && !stat.IsDir() {
		if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			execCmd("sudo", []string{"rm", "-f", target}, fmt.Sprintf("failed to remove bash completions %s", target))
		}
	}
}

func removeCASFS() {
	execCmd("sudo", []string{"modprobe", "-r", "casfs"}, "failed to remove casfs kernel module")
}

func removeBaseDirConcurrently(ctx context.Context) error {
	baseDir := os.Getenv("ABFS_BASE_DIR")
	if baseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		baseDir = filepath.Join(home, ".abfs")
	}

	if _, err := os.Stat(baseDir); err != nil {
		return nil
	}

	numWorkers := runtime.NumCPU()
	toRemove := make(chan string, numWorkers*4)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for i := 0; i < numWorkers; i++ {
		go func() {
			defer wg.Done()
			for p := range toRemove {
				if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
					log.Printf("Warning: failed to remove file %s: %v", p, err)
				}
			}
		}()
	}

	err := cwalk.Walk(baseDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !info.IsDir() {
			toRemove <- filepath.Join(baseDir, path)
		}
		return nil
	})

	close(toRemove)
	wg.Wait()

	if err != nil {
		return err
	}

	if err := os.RemoveAll(baseDir); err != nil {
		log.Printf("Warning: failed to remove base directory %s: %v", baseDir, err)
		return err
	}
	return nil
}
