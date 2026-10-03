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

package tap

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"

	"github.com/google/abfs/binding/run"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

const (
	CtrlDir            = ".ctrl"
	TapCtrlDir         = "tap"
	tapStdinThreashold = 10000
	MaxChunkSize       = 8192
	ChannelMultiplier  = 4
	MinimumChannel     = 64
)

func findTap(dir string) (string, error) {
	dirName := path.Join(dir, CtrlDir, TapCtrlDir)
	if st, err := os.Stat(dirName); err == nil {
		if st.IsDir() {
			return dirName, nil
		}
	}
	if dir == "/" {
		return "", fmt.Errorf("failed to find path to tapping directory (usually something like <mount>/.ctrl/tap)")
	}
	return findTap(filepath.Dir(dir))
}

func baseTapDir(outDir string) (string, error) {
	var out string
	if outDir == "" {
		out = os.Getenv("OUT_DIR")
		if out == "" {
			out = "out"
		}
	} else {
		out = outDir
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return path.Join(wd, out), nil
}

func RunTapCmd(cmd *exec.Cmd, tapPath string, showRaw bool, prefix string, strict bool, outDir string, snapshotConcurrency int) (tapResult *entities.TapCmdResult, retErr error) {
	if err := syscall.Setpgid(0, 0); err != nil {
		return nil, err
	}

	if tapPath == "" {
		// Search for tap path
		tapDir, err := baseTapDir(outDir)
		if err != nil {
			return nil, err
		}
		tapPath, err = findTap(tapDir)
		if err != nil {
			return nil, err
		}
	}
	fd, err := unix.Open(tapPath, unix.O_RDWR|unix.O_TMPFILE|unix.O_EXCL|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open tmp file for dir %s (%s)", tapPath, err)
	}
	defer func() {
		retErr = entities.FirstError(retErr, unix.Close(fd))
	}()

	// Spawn reading of tap data
	var wg sync.WaitGroup
	var decodeErr, patchErr error
	wg.Add(2)

	channelCapacity := runtime.NumCPU() * ChannelMultiplier
	if channelCapacity < MinimumChannel {
		channelCapacity = MinimumChannel
	}
	taps := make(chan *entities.TapFSEntry, channelCapacity)
	tapReader := tapReaderCreate(fd)

	go func() {
		defer wg.Done()
		defer close(taps)

		decodeErr = TapDecode(tapReader, taps)
	}()
	snap := &snapshot{prefix: prefix, hashStat: bindcas.HashStat}
	tapsPatch := newTapsPatch(snap, strict, outDir)

	go func() {
		defer wg.Done()
		defer func() {
			// Drain taps in case of panic
			for range taps {
			}
		}()
		patchErr = tapsPatch.update(showRaw, taps)
	}()

	r := run.RunCommand(cmd)

	if err := tapReader.CmdDone(); err != nil {
		return nil, err
	}
	wg.Wait()
	if decodeErr != nil {
		return nil, decodeErr
	}
	if patchErr != nil {
		return nil, patchErr
	}
	if r.Err != nil && r.ExitCode == 0 {
		return nil, r.Err
	}

	tapResult = &entities.TapCmdResult{
		Pid:         r.Pid,
		ExitCode:    r.ExitCode,
		Rusage:      r.Rusage,
		Timestamp:   r.Timestamp,
		RunDuration: r.RunDuration,
		Stdout:      string(r.Stdout),
		Stderr:      r.Stderr,
	}

	if err := updateResult(tapResult, tapsPatch, snap, snapshotConcurrency); err != nil {
		return nil, err
	}

	return tapResult, nil
}

func entriesToPatch(entries map[string]*entities.PatchEntry) entities.Patch {
	result := make(entities.Patch, 0, len(entries))
	for _, p := range entries {
		result = append(result, p)
	}
	return result
}

func updateResult(tapResult *entities.TapCmdResult, s *tapsPatch, snap *snapshot, concurrency int) error {
	tapResult.ReadPatch = entriesToPatch(s.reads)
	tapResult.Deletes = make([]string, 0, len(s.deletes))
	for p := range s.deletes {
		tapResult.Deletes = append(tapResult.Deletes, p)
	}
	tapResult.TapCounts = make(map[string]uint64)
	for k, v := range s.tapCounts {
		tapResult.TapCounts[k.String()] = v
	}
	snapStart := time.Now()
	defer func() {
		tapResult.SnapshotDuration = time.Since(snapStart).Seconds()
	}()

	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
	}

	type walkItem struct {
		path string
		mode fs.FileMode
	}

	var items []walkItem
	if err := s.tracker.Walk(func(path string, mode fs.FileMode) error {
		items = append(items, walkItem{path: path, mode: mode})
		return nil
	}); err != nil {
		return err
	}

	tapResult.WritePatch = make([]*entities.PatchEntry, len(items))

	var grp errgroup.Group
	grp.SetLimit(concurrency)

	for i, item := range items {
		if item.mode.IsDir() {
			tapResult.WritePatch[i] = &entities.PatchEntry{
				Path: item.path,
				Mode: entities.ModeToFileMode(item.mode),
				Hash: entities.ZeroHash,
				Size: 0,
			}
			continue
		}

		grp.Go(func() error {
			pe, err := snap.Snapshot(item.path)
			if err != nil {
				return err
			}
			tapResult.WritePatch[i] = pe
			return nil
		})
	}

	return grp.Wait()
}

func TapCommand(cmd string, chroot, outDir string, strictTappingOff bool, snapshotConcurrency int) (entities.TapCmdResult, error) {
	var result entities.TapCmdResult

	var tapPath string
	switch runtime.GOOS {
	case "linux":
		tapPath = "./prebuilts/build-tools/linux-x86/bin/tap"
	case "darwin":
		tapPath = "./prebuilts/build-tools/darwin-x86/bin/tap"
	default:
		return result, fmt.Errorf("unsupported os (%s) for tapping", runtime.GOOS)
	}

	proc := run.BuildCommand(cmd)

	tapOptions := []string{"--prefix", chroot}
	if !strictTappingOff {
		tapOptions = append([]string{"--strict"}, tapOptions...)
	}

	// The old version of android the bootstrap in ninja had a different builddir (`out/soong`)
	if os.Getenv(entities.OutDirVar) == "" {
		tapOptions = append([]string{"--outdir", outDir}, tapOptions...)
	}
	tapOptions = append([]string{"--snapshot-concurrency", fmt.Sprintf("%d", snapshotConcurrency)}, tapOptions...)
	tapCmd := exec.Command(tapPath, tapOptions...)
	if len(cmd) > tapStdinThreashold {
		tapCmd.Args = append(tapCmd.Args, "--stdin", "--", "/bin/sh")
		tapCmd.Stdin = strings.NewReader(cmd)
	} else {
		tapCmd.Args = append(tapCmd.Args, "--")
		tapCmd.Args = append(tapCmd.Args, proc.Args...)
	}
	r := run.RunCommand(tapCmd)
	if r.Err != nil {
		return result, r.Err
	}

	if err := json.Unmarshal([]byte(r.Stdout), &result); err != nil {
		return result, err
	}
	return result, nil
}
