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

package builder

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
	"strings"
	"time"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/entities"
)

const (
	BuildStartOutFile    = ".build_start"
	BuildInfoOutFile     = ".build_info"
	MountDetailsFile     = ".repo/mount-details"
	ABFSMountDetailsFile = ".abfs.mount-details"
	ChrootName           = "chroot"
	cacheIdleWaitTimeout = 30 * time.Minute
)

type Hash = entities.Hash

type buildWorker struct {
	abfsBinDir string
	// `abfs` bin and extra flags, e.g. `abfs --cache-dir /path/to/cache`
	abfsRootCommand []string
	store           entities.CASStore
	skipHealthCheck bool
	// skipRefPrefix can be useful when running in a staging environment or other environment where
	// you want to process requests but not have the results stored at a ref
	skipRefPrefix bool
}

func (w *buildWorker) processRequest(ctx context.Context, h Hash) (returnErr error) {
	log.Printf("worker processing request %q", h)
	requestCounter.Inc()

	b, err := w.store.GetObject(ctx, h)
	if err != nil {
		return fmt.Errorf("failed to find object with hash %q in the store: %w", h, err)
	}

	req := &binding.BuildRequest{}
	if err := req.Decode(h, b); err != nil {
		return fmt.Errorf("parsing data from hash %q into a buildrequest: %w", h, err)
	}

	var buildStartHash, buildInfoHash, mountDetailsHash, unmountDetailsHash Hash
	var uploadDuration time.Duration
	defer func() {
		w.writeBuildResult(h, mountDetailsHash, buildStartHash, buildInfoHash, unmountDetailsHash, uploadDuration, returnErr)
	}()

	// TODO: validate request
	mountDir, mkDirErr := os.MkdirTemp("", "buildmanworker-mount-*")
	defer func() { // we want to try deleting the directory even if creating it failed, in case it was partially created
		if err := os.Remove(mountDir); mkDirErr == nil && err != nil {
			log.Printf("WARNING: failed to delete temp directory %q: %v", mountDir, err)
		}
	}()
	if mkDirErr != nil {
		return mkDirErr
	}

	mountErr := w.mount(ctx, mountDir, req.PinnedManifestHash, req.RefPrefix)
	defer func() { // we want to try unmounting even if mounting failed, in case it was partially mounted
		var unmountErr error
		// we want to pass a new context here so that we still try to unmount if the context was already cancelled
		unmountDetailsHash, unmountErr = w.unmount(context.Background(), mountDir)
		if mountErr == nil && unmountErr != nil {
			log.Printf("WARNING: failed to unmount directory %q: %v", mountDir, err)
		}
	}()
	if mountErr != nil {
		return mountErr
	}

	_, mountDetailsHash, err = readMountDetailsHash(mountDir)
	if err != nil {
		return err
	}

	var outputPath string
	if req.NSJailBuild.Enabled {
		if err := w.nsJailBuild(ctx, mountDir, req.BuildCommand, req.NSJailBuild.Overlays); err != nil {
			return err
		}

		outputPath = path.Join(mountDir, ChrootName)
	} else {
		if err := w.regularBuild(ctx, mountDir, req.BuildCommand); err != nil {
			return err
		}

		outputPath = mountDir
	}

	// measure the upload time right after the build completes so it's as accurate as possible
	uploadStart := time.Now()
	if err := waitCacheIdle(ctx, cacheIdleWaitTimeout); err != nil {
		log.Printf("WARNING: %q failed to wait for cache to be idle after %s: %v", h, cacheIdleWaitTimeout, err)
		uploadDuration = cacheIdleWaitTimeout
	} else {
		uploadDuration = time.Since(uploadStart)
	}

	// if soong panics or otherwise doesn't finish, we could fail to find the hashes here without it being a buildman error
	buildStartHash, err = readBuildStartHash(outputPath)
	if err != nil {
		log.Printf("WARNING: %q failed to read build start hash from output path %q: abfsbox could have failed. err: %v", h, outputPath, err)
	}
	buildInfoHash, err = readBuildInfoHash(outputPath)
	if err != nil {
		return fmt.Errorf("failed to find buildinfo file. Soong or abfsbox could have failed. err: %w", err)
	}

	return nil
}

func (w *buildWorker) writeBuildResult(requestHash Hash, mountDetailsHash, buildStartHash, buildInfoHash, unmountDetailsHash Hash, uploadDuration time.Duration, returnErr error) {
	debugLog("creating a BuildResult with MountDetails %q, BuildStart %q, BuildInfos %q, UnmountDetails %q, UploadDuration %q, Err %q", mountDetailsHash, buildStartHash, buildInfoHash, unmountDetailsHash, uploadDuration, returnErr)
	var errStr string
	if returnErr != nil {
		errorCounter.Inc()
		errStr = returnErr.Error()
	}
	buildHashes := make([]binding.BuildHash, 0, 1)
	if !buildStartHash.IsZero() || !buildInfoHash.IsZero() {
		successCounter.Inc()
		buildHashes = append(buildHashes, binding.BuildHash{BuildStart: buildStartHash, BuildInfo: buildInfoHash})
	}
	br := &binding.BuildResult{
		Version:        binding.CurrentBuildResultVersion,
		BuildRequest:   requestHash,
		MountDetails:   mountDetailsHash,
		BuildHashes:    buildHashes,
		UnmountDetails: unmountDetailsHash,
		UploadSecs:     uploadDuration.Seconds(),
		Err:            errStr,
	}
	if err := br.Encode(); err != nil {
		log.Printf("WARNING: failed to encode buildresult %v: %v", br, err)
		return
	}
	debugLog("storing BuildResult with hash %q", br.Hash)
	// new context so we can store the result even if it's canceled
	if err := w.store.PutObject(context.Background(), br.Hash, br.Encoded); err != nil {
		log.Printf("WARNING: failed to store buildresult %v: %v", br, err)
		return
	}

	// map build request to build result
	if err := w.store.AddMapResult(context.Background(), requestHash, br.Hash); err != nil {
		log.Printf("WARNING: failed to map build request %q to buildresult %q: %v", requestHash, br.Hash, err)
		return
	}
	log.Printf("processed request %q and mapped it to result %q", requestHash, br.Hash)
}

func (w *buildWorker) mount(ctx context.Context, dir string, manifestHash Hash, refPrefix string) error {
	debugLog("mounting manifestHash %q", manifestHash)
	cmdArgs := append(w.abfsRootCommand, "mount", "--tmp-ws", "--manifest-hash", manifestHash.String())

	if !w.skipRefPrefix && refPrefix != "" {
		cmdArgs = append(cmdArgs, "--ref-prefix", refPrefix)
	}

	if w.skipHealthCheck {
		cmdArgs = append(cmdArgs, "--skip-health-check")
	}

	cmdArgs = append(cmdArgs, dir)
	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount command %q failed. err: %w, output: %s", strings.Join(cmd.Args, " "), err, out)
	}
	return nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (w *buildWorker) nsJailBuild(ctx context.Context, mountDir, buildCommand string, overlays []string) error {
	debugLog("doing an nsjail build in dir %q with buildCommand %q, and overlays %s", mountDir, buildCommand, overlays)
	cmdArgs := append(w.abfsRootCommand, "nsjail", "--top", mountDir, "--chroot", ChrootName, "--compiled", w.abfsBinDir)
	if len(overlays) > 0 {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--overlays=%q", strings.Join(overlays, ",")))
	}
	cmdArgs = append(cmdArgs, fmt.Sprintf("'%s'", buildCommand))

	if err := w.buildWithTempFileOutput(ctx, mountDir, strings.Join(cmdArgs, " ")); err != nil {
		return err
	}
	debugLog("nsjail build in dir %q completed", mountDir)
	return nil
}

func (w *buildWorker) regularBuild(ctx context.Context, mountDir, buildCommand string) error {
	debugLog("doing a regular build in dir %q with buildCommand %q", mountDir, buildCommand)
	cmdArgs := append(w.abfsRootCommand, "setup", mountDir)
	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// TODO: a build error isn't necessarily an error in this automatic builder program so check output here
		start := max(0, len(out)-500)
		return fmt.Errorf("setup command %q failed. err: %w, end of output: %s", strings.Join(cmd.Args, " "), err, out[start:])
	}
	debugLog("setup command succeeded")

	if err := w.buildWithTempFileOutput(ctx, mountDir, buildCommand); err != nil {
		return err
	}
	debugLog("regular build in dir %q completed", mountDir)
	return nil
}

func (w *buildWorker) buildWithTempFileOutput(ctx context.Context, mountDir, buildCommand string) error {
	f, err := os.CreateTemp("", "buildmanworker-build-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file for build log, err: %w", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("WARNING: failed to close log file %q: %v", f.Name(), err)
			return
		}
		// TODO: store the temp log file with the build result then delete it from the local disk
	}()
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", buildCommand)
	// Ideally these env vars wouldn't be necessary, but there are some cases where build rules use these, so they lead to cache misses.
	// These env vars won't be reflected in the nsjail builds because nsjail cleans the envrionment.
	cmd.Env = append(os.Environ(), "BUILD_NUMBER=eng", "BUILD_HOSTNAME=abfs-builder", "SOONG_INCREMENTAL_ANALYSIS=false", "RBE_use_application_default_credentials=true")
	cmd.Dir = mountDir
	cmd.Stdout = f
	cmd.Stderr = f
	log.Printf("Starting build command with log file %q", f.Name())
	err = cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			log.Printf("not returning build error as a buildman error: %v", exitErr)
		} else {
			return fmt.Errorf("build command %q in dir %q failed. err: %w", buildCommand, mountDir, err)
		}
	}
	return nil
}

func (w *buildWorker) unmount(ctx context.Context, mountDir string) (Hash, error) {
	debugLog("unmounting %q with --kill option", mountDir)
	cmdArgs := append(w.abfsRootCommand, "unmount", "--kill", "--hash", mountDir)
	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return Hash{}, fmt.Errorf("unmount command %q failed. err: %w, stdout: %q, stderr: %q", strings.Join(cmd.Args, " "), err, stdout, stderr)
	}

	hashStr := strings.TrimSpace(stdout.String())
	if !entities.IsHash(hashStr) {
		return Hash{}, fmt.Errorf("stdout from unmount command %q was not a hash", hashStr)
	}
	return entities.NewHash(hashStr), nil
}

func readBuildStartHash(buildPath string) (hash Hash, retErr error) {
	m := struct {
		*binding.BuildStart
		Hash Hash
	}{}
	f, err := os.Open(path.Join(buildPath, entities.OutDir, BuildStartOutFile))
	if err != nil {
		return Hash{}, err
	}
	defer func() { retErr = entities.FirstError(retErr, f.Close()) }()
	if err := json.NewDecoder(f).Decode(&m); err != nil {
		return Hash{}, err
	}
	buildStartTimestampMetric.Set(float64(m.Start.Unix()))
	return m.Hash, nil
}

func readBuildInfoHash(buildPath string) (hash Hash, retErr error) {
	m := struct {
		*binding.BuildInfo
		Hash Hash
	}{}
	f, err := os.Open(path.Join(buildPath, entities.OutDir, BuildInfoOutFile))
	if err != nil {
		return Hash{}, err
	}
	defer func() { retErr = entities.FirstError(retErr, f.Close()) }()
	if err := json.NewDecoder(f).Decode(&m); err != nil {
		return Hash{}, err
	}
	buildTime := m.End.Sub(m.Start).Seconds()
	buildInfoTimeMetric.Observe(buildTime)

	if len(m.NinjaRunInfo) > 0 {
		ninjaFirstRunCacheHitMetric.Observe(m.NinjaRunInfo[0].CacheHitPercent)
		if len(m.NinjaRunInfo) > 1 {
			ninjaSecondRunCacheHitMetric.Observe(m.NinjaRunInfo[1].CacheHitPercent)
		}
	}

	return m.Hash, nil
}

func readMountDetailsHash(mountDir string) (d *binding.MountDetails, hash Hash, retErr error) {
	m := struct {
		*binding.MountDetails
		Hash Hash
	}{}
	var openErr error
	for _, relPath := range []string{MountDetailsFile, ABFSMountDetailsFile} {
		f, err := os.Open(path.Join(mountDir, relPath))
		if err != nil {
			openErr = err
			continue
		}
		defer func(f *os.File) { retErr = entities.FirstError(retErr, f.Close()) }(f)
		if err := json.NewDecoder(f).Decode(&m); err != nil {
			return nil, Hash{}, err
		}
		return m.MountDetails, m.Hash, nil
	}
	return nil, Hash{}, openErr
}

func waitCacheIdle(ctx context.Context, timeout time.Duration) error {
	tSecs := int64(timeout.Seconds())
	cmd := exec.CommandContext(ctx, "abfs", "cacheman", "wait", "-t", fmt.Sprintf("%d", tSecs))
	if err := cmd.Run(); err != nil {
		if err, ok := err.(*exec.ExitError); ok && err.ExitCode() == 1 {
			return fmt.Errorf("cache wasn't idle after waiting %d seconds", tSecs)
		}
		return fmt.Errorf("cacheman wait command failed. err: %w", err)
	}
	return nil
}
