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

package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"golang.org/x/sys/unix"

	"github.com/google/abfs/shared/user"
	"github.com/google/abfs/shared/version"
)

const DefaultFetchTimeout = 2 * time.Minute

var (
	umount       bool
	local        bool
	partialClone bool
	cacheDir     string
	fetchTimeout int
	showVersion  bool
)

func main() {
	pflag.BoolVarP(&umount, "umount", "u", false, "Umount the given mount point")
	pflag.BoolVarP(&local, "local", "l", false, "Only use the local objects")
	pflag.BoolVarP(&partialClone, "partial-clone", "p", false, "Enable partial clone")
	pflag.StringVarP(&cacheDir, "cache-dir", "c", "", "The CASFS cache directory")
	pflag.IntVar(&fetchTimeout, "fetch-timeout", int(DefaultFetchTimeout.Milliseconds()), "The timeout for fetching the remote object")
	pflag.BoolVar(&showVersion, "version", false, "Print the application version")
	pflag.Parse()

	if showVersion {
		fmt.Println(version.Version())
		return
	}

	args := pflag.Args()
	if len(args) != 1 {
		log.Fatalf("Expected a single mount point!\nUsage: %s [-u] [-l] [-c <cache-dir>] <mount-point>", os.Args[0])
	}
	mntPoint := args[0]

	err := validate(mntPoint)
	if err != nil {
		log.Fatalf("Mount point is not valid: %v", err)
	}

	if umount {
		err = doUmount(mntPoint)
	} else {
		err = doMount(mntPoint)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func validate(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}

	if fi.Mode()&0700 != 0700 {
		return fmt.Errorf("permission denied on %s", dir)
	}

	currUser, err := user.CurrentUser()
	if err != nil {
		return err
	}
	if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
		if fmt.Sprintf("%d", stat.Uid) != currUser.Uid {
			return fmt.Errorf("%s is owned by a different user", dir)
		}
	} else {
		return fmt.Errorf("unexpected stat information on %s", dir)
	}

	return nil
}

func doUmount(mntPoint string) error {
	var statfs syscall.Statfs_t
	err := syscall.Statfs(mntPoint, &statfs)
	if err != nil {
		return err
	}

	if statfs.Type != 0xca5f5000 {
		return fmt.Errorf("the mounted file system on %s is not casfs", mntPoint)
	}

	if err := syscall.Unmount(mntPoint, 0); err != nil {
		return fmt.Errorf("umount syscall failed: %w", err)
	}
	return nil
}

func doMount(mntPoint string) error {
	if cacheDir == "" {
		return errors.New("--cache-dir must be provided")
	}

	if err := ensureCasfsModule(); err != nil {
		return err
	}

	if err := validate(cacheDir); err != nil {
		return fmt.Errorf("cache directory not valid: %w", err)
	}

	fsfd, err := unix.Fsopen("casfs", 0)
	if err != nil {
		return doMountLegacy(mntPoint)
	}
	defer unix.Close(fsfd)

	getKerrMsg := func() string {
		buf := make([]byte, 1024)
		n, err := unix.Read(fsfd, buf)
		if err != nil || n <= 0 {
			return ""
		}

		raw := string(buf[:n])

		// Locate "casfs:" in the buffer to discard kernel header prefixes like "<3>" or "e "
		if idx := strings.Index(raw, "casfs:"); idx != -1 {
			msg := raw[idx:]
			// Clean up any trailing newlines or null bytes
			msg = strings.TrimRight(msg, "\r\n\x00 ")
			return msg
		}

		return strings.TrimSpace(raw)
	}

	opts := map[string]string{
		"cache": cacheDir,
	}
	if local {
		opts["local"] = ""
	}
	if partialClone {
		opts["partial-clone"] = ""
	}
	if fetchTimeout != int(DefaultFetchTimeout.Milliseconds()) {
		opts["fetch-timeout"] = fmt.Sprintf("%d", fetchTimeout)
	}

	for k, v := range opts {
		var err error
		if v == "" {
			err = unix.FsconfigSetFlag(fsfd, k)
		} else {
			err = unix.FsconfigSetString(fsfd, k, v)
		}
		if err != nil {
			if kmsg := getKerrMsg(); kmsg != "" {
				return fmt.Errorf("fsconfig(%s) failed: %s", k, kmsg)
			}
			return fmt.Errorf("fsconfig(%s) failed: %w", k, err)

		}
	}

	if err := unix.FsconfigCreate(fsfd); err != nil {
		if kmsg := getKerrMsg(); kmsg != "" {
			return errors.New(kmsg)
		}
		return fmt.Errorf("fsconfig create failed: %w", err)
	}

	mfd, err := unix.Fsmount(fsfd, 0, 0)
	if err != nil {
		return fmt.Errorf("fsmount failed: %w", err)
	}
	defer unix.Close(mfd)

	if err := unix.MoveMount(mfd, "", unix.AT_FDCWD, mntPoint, unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
		return err
	}

	return nil
}

func doMountLegacy(mntPoint string) error {
	opts := fmt.Sprintf("cache=%s", cacheDir)
	if local {
		opts += ",local"
	}
	if partialClone {
		opts += ",partial-clone"
	}
	if fetchTimeout != int(DefaultFetchTimeout.Milliseconds()) {
		opts += fmt.Sprintf(",fetch-timeout=%d", fetchTimeout)
	}

	if err := syscall.Mount("none", mntPoint, "casfs", 0, opts); err != nil {
		return fmt.Errorf("mount syscall failed: %w", err)
	}
	return nil
}

func ensureCasfsModule() error {
	const versionPath = "/sys/module/casfs/version"
	if _, err := os.Stat(versionPath); err == nil {
		return nil
	}

	// Raise ambient CAP_SYS_MODULE to ensure modprobe inherits
	if err := raiseAmbientCap(unix.CAP_SYS_MODULE); err != nil {
		log.Printf("Warning: failed to raise ambient CAP_SYS_MODULE: %v", err)
	}

	if output, err := exec.Command("modprobe", "casfs").CombinedOutput(); err != nil {
		return fmt.Errorf("failed to insert casfs output: %v, err: %v", strings.TrimSpace(string(output)), err)
	} else {
		log.Print("casfs kernel module inserted")
	}

	return nil
}

func raiseAmbientCap(cap int) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hdr := &unix.CapUserHeader{
		Version: unix.LINUX_CAPABILITY_VERSION_3,
		Pid:     0, // 0 means the calling process
	}

	// Version 3 uses 2 32-bit pieces of data
	var data [2]unix.CapUserData
	if err := unix.Capget(hdr, &data[0]); err != nil {
		return fmt.Errorf("capget failed: %w", err)
	}

	idx := cap / 32
	shift := uint(cap % 32)

	// Check if we have it in Permitted
	if (data[idx].Permitted & (1 << shift)) == 0 {
		return fmt.Errorf("capability %d is not in permitted set", cap)
	}

	// Add to Inheritable so that the Ambient apply works
	data[idx].Inheritable |= (1 << shift)

	if err := unix.Capset(hdr, &data[0]); err != nil {
		return fmt.Errorf("capset failed: %w", err)
	}

	// Raise ambient to ensure child process inherits into their permitted and effective capabilities
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_RAISE, uintptr(cap), 0, 0); err != nil {
		return fmt.Errorf("prctl PR_CAP_AMBIENT_RAISE failed: %w", err)
	}

	return nil
}
