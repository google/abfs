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

package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/abfs/abfslog"
	"github.com/google/abfs/binding"
	"github.com/google/abfs/binding/run"
	"github.com/google/abfs/cmd/abfs/nsjail"
	"github.com/google/abfs/daemon"
	"github.com/google/abfs/ninjago"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
)

const (
	tapBinPath         = "/tap"
	nsJailBin          = "prebuilts/build-tools/linux-x86/bin/nsjail"
	nsJailCfg          = "tools/treble/build/sandbox/nsjail.cfg"
	objectFetchTimeout = 30 * time.Minute
)

var (
	// Checked in July 2025 and these hashes existed on the production servers.
	// We could allow alternate methods like providing a mounted root directory, root hash, branch name, etc.
	// Then we could pull these files from those instead of the hardcoded hashes.
	nsjailPatch = entities.Patch{
		&entities.PatchEntry{
			Path: nsJailCfg,
			Mode: entities.FileModeRegular,
			Hash: entities.NewHash("5341577934dae45f7257a71643f6fe7aac595630"),
		},
		&entities.PatchEntry{
			Path: nsJailBin,
			Mode: entities.FileModeExecutable,
			Hash: entities.NewHash("97eb5348b7eb6854c3c0f3098d87b4835f5b1932"),
		},
		&entities.PatchEntry{
			Path: "prebuilts/build-tools/linux-x86/lib64/libc++.so",
			Mode: entities.FileModeExecutable,
			Hash: entities.NewHash("9fcc479678bea287d3f876c9fe5f9a29fc3ef14c"),
		},
		&entities.PatchEntry{
			Path: "prebuilts/build-tools/linux-x86/lib64/libnl.so",
			Mode: entities.FileModeExecutable,
			Hash: entities.NewHash("0c6adb5389477d9a8b7a01e67c161d085a6238c7"),
		},
		&entities.PatchEntry{
			Path: "prebuilts/build-tools/linux-x86/lib64/libprotobuf-cpp-full.so",
			Mode: entities.FileModeExecutable,
			Hash: entities.NewHash("890fdccabdd926302aab646c0b68c0c01f1f6a7e"),
		},
		&entities.PatchEntry{
			Path: "prebuilts/build-tools/linux-x86/lib64/libz-host.so",
			Mode: entities.FileModeExecutable,
			Hash: entities.NewHash("a7c38070123decf6bf44ae29296ca74741338e9b"),
		},
	}
)

type SetupInput struct {
	ABFSConfig

	// path of tap binary outside of ABFS
	TapPath string
	// Keep filesystem artifacts alive after closing.
	Keep bool
}

type ABFSConfig struct {
	// subset of values from ABFS config
	FSType       instance.CASFSType
	LocalOnly    bool
	CacheDir     string
	FetchTimeout time.Duration
}

type Evaluator struct {
	SetupInput

	store    entities.CASStore
	cacheman *client.Client
	// could move to having a pool of multiple roots/mounts to cleanly separate parallel executing requests
	rootDir      string
	mntPoint     string
	tapPath      string
	client       *binding.Client
	backgroundWG sync.WaitGroup

	setupCleanup func() error

	activeJobs sync.Map
}

// JobStatus represents the state of an evaluation job.
type JobStatus string

const (
	JobStatusFetch       JobStatus = "fetch"
	JobStatusPrepare     JobStatus = "prepare"
	JobStatusRun         JobStatus = "run"
	JobStatusPostProcess JobStatus = "post-process"
	JobStatusError       JobStatus = "error"
	JobStatusSuccess     JobStatus = "success"
)

type ActiveJob struct {
	ReqHash   entities.Hash
	Patches   []entities.Hash
	StartTime time.Time
	Status    JobStatus
	Command   string
	Durations JobDurations
}

type JobDurations struct {
	Fetch   time.Duration
	Prepare time.Duration
	Run     time.Duration
	Post    time.Duration
}

func (e *Evaluator) Close() error {
	log.Println("Evaluator closing - waiting for background tasks to finish")
	e.backgroundWG.Wait()
	log.Println("Evaluator closing - background tasks finished, cleaning up setup")
	err := e.setupCleanup()
	log.Printf("Evaluator closed, returning err=%v", err)
	return err
}

// NewEvaluator creates a temporary directory, mounts casfs at /mnt inside that directory,
// applies a hard-coded NSJail patch to the mount to prepare NSJail for use, and copies tap
// into the mount for ABFS-copying later on.
func NewEvaluator(store entities.CASStore, cacheman *client.Client, in SetupInput) (*Evaluator, error) {
	cleanupFuncs := []func() error{}
	cleanup := func() error {
		errs := make([]error, 0, len(cleanupFuncs))
		// loop backwards for defer-like functionality
		for i := len(cleanupFuncs) - 1; i >= 0; i-- {
			errs = append(errs, cleanupFuncs[i]())
		}
		return errors.Join(errs...)
	}

	rootDir, err := os.MkdirTemp("", "eval-mnt-*")
	if err != nil {
		return nil, fmt.Errorf("cannot create temp dir for mounting: %w", err)
	}
	cleanupFuncs = append(cleanupFuncs, func() error {
		if in.Keep {
			log.Printf("Keeping dir %q because Keep option was specified", rootDir)
		} else {
			if err := os.RemoveAll(rootDir); err != nil {
				return fmt.Errorf("cannot remove the tmp dir %q: %w", rootDir, err)
			}
		}
		return nil
	})

	mntPoint := path.Join(rootDir, "/mnt")
	if err := os.MkdirAll(mntPoint, 0755); err != nil {
		if err := cleanup(); err != nil {
			return nil, fmt.Errorf("cannot cleanup after failing to create dir %s: %w", mntPoint, err)
		}
		return nil, fmt.Errorf("cannot create dir %s: %w", mntPoint, err)
	}
	mountCmd, err := mount(mntPoint, in.FSType)
	if err != nil {
		if err := cleanup(); err != nil {
			return nil, fmt.Errorf("cannot cleanup after failing to mount: %w", err)
		}
		return nil, err
	}
	cleanupFuncs = append(cleanupFuncs, func() error {
		if in.Keep {
			log.Printf("Keeping abfs mount at %q because Keep option was specified", mntPoint)
			return nil
		}
		return unmount(mntPoint, mountCmd)
	})

	socketFile := path.Join(mntPoint, entities.ABFSSocketFileName)
	if err := daemon.WaitForFile(socketFile, 5*time.Second); err != nil {
		if err := cleanup(); err != nil {
			return nil, fmt.Errorf("cannot cleanup after failing to wait for socket file: %w", err)
		}
		return nil, err
	}
	client, err := binding.NewClient(mntPoint, entities.ABFSSocketFileName, in.CacheDir, in.LocalOnly, in.FetchTimeout)
	if err != nil {
		if err := cleanup(); err != nil {
			return nil, fmt.Errorf("cannot cleanup after failing to create abfs client: %w", err)
		}
		return nil, fmt.Errorf("cannot create abfs client: %w", err)
	}
	cleanupFuncs = append(cleanupFuncs, func() error {
		if err := client.Close(); err != nil {
			return fmt.Errorf("error while closing abfs client: %w", err)
		}
		return nil
	})

	if err := client.ApplyPatch(nil, nsjailPatch, nil, ""); err != nil {
		if err := cleanup(); err != nil {
			return nil, fmt.Errorf("cannot cleanup after failing to apply the nsjail patch: %w", err)
		}
		return nil, fmt.Errorf("failed to apply the nsjail patch: %w", err)
	}

	// Couldn't get tap to be executable with an NSJail binding, so we're copying it into the mount and then doing an ABFS copy into each needed location
	tapPath := path.Join(mntPoint, tapBinPath)
	if err := copyExecutableFile(in.TapPath, tapPath); err != nil {
		if err := cleanup(); err != nil {
			return nil, fmt.Errorf("cannot cleanup after failing to copy tap: %w", err)
		}
		return nil, err
	}
	return &Evaluator{
		SetupInput:   in,
		cacheman:     cacheman,
		store:        store,
		rootDir:      rootDir,
		mntPoint:     mntPoint,
		tapPath:      tapPath,
		client:       client,
		setupCleanup: cleanup,
	}, nil
}

type EvalOptions struct {
	// Whether to store the eval response in the store
	WriteEvalResponse bool
	// Whether to store the eval response as a map result of the eval request
	StoreMapResult bool
	// Whether to prefetch the inputs of the eval request
	PrefetchInputs bool
}

// Eval calls the mount's Chroot function to establish a temporary directory for execution. This is used
// as the NSJail chroot so the linux OS directories /lib, /bin, /usr are placed there via NSJail read-only binds.
// The Android tree is rooted at /src inside the chroot. Tap is placed at /tap inside. The whole directory
// is removed afterwards.
func (e *Evaluator) Eval(ctx context.Context, h entities.Hash, patchHashes []entities.Hash, opt EvalOptions) (_ *binding.EvalResponse, _ *ActiveJob, returnErr error) {
	jobID := fmt.Sprintf("%s-%d", h, rand.Int())
	job := &ActiveJob{
		ReqHash:   h,
		Patches:   patchHashes,
		StartTime: time.Now(),
		Status:    JobStatusFetch,
		Durations: JobDurations{},
	}
	prev := job.StartTime
	e.activeJobs.Store(jobID, job)

	defer e.activeJobs.Delete(jobID)

	updateStatus := func(status JobStatus) {
		switch job.Status {
		case JobStatusFetch:
			job.Durations.Fetch = time.Since(prev)
		case JobStatusPrepare:
			job.Durations.Prepare = time.Since(prev)
		case JobStatusRun:
			job.Durations.Run = time.Since(prev)
		case JobStatusPostProcess:
			job.Durations.Post = time.Since(prev)
		default:
			log.Printf("WARNING: Evaluator.Eval job unknown previous status %q", job.Status)
		}
		prev = time.Now()
		job.Status = status
		e.activeJobs.Store(jobID, job)
	}

	defer func() {
		if returnErr != nil {
			updateStatus(JobStatusError)
		} else {
			updateStatus(JobStatusSuccess)
		}
	}()

	req, err := binding.FetchObjectWarnOnRetry[binding.EvalRequest](ctx, e.store, h, objectFetchTimeout)
	if err != nil {
		return nil, job, fmt.Errorf("cannot fetch eval-request %q: %w", h, err)
	}
	job.Command = req.Command
	e.activeJobs.Store(jobID, job)

	if opt.PrefetchInputs {
		if err := binding.PrefetchInputPatch(req.InputPatch, e.cacheman); err != nil {
			return nil, job, fmt.Errorf("cannot prefetch inputs for eval-request %q: %w", h, err)
		}
	}

	patches := make([]*binding.PatchObject, 0, len(patchHashes))
	for _, h := range patchHashes {
		p, err := binding.FetchObjectWarnOnRetry[binding.PatchObject](ctx, e.store, h, objectFetchTimeout)
		if err != nil {
			return nil, job, fmt.Errorf("cannot fetch patch %q: %w", h, err)
		}
		patches = append(patches, p)
	}

	updateStatus(JobStatusPrepare)

	chrootDirName := fmt.Sprintf("chroot-%d", rand.Int64())
	if err := e.client.Chroot(chrootDirName, []string{}, []string{}); err != nil {
		return nil, job, fmt.Errorf("cannot create chroot directory %q in abfs mount: %w", chrootDirName, err)
	}
	absChrootDir := path.Join(e.mntPoint, chrootDirName)

	defer func() {
		if e.Keep {
			log.Printf("Keeping chroot dir %q because Keep option was specified", absChrootDir)
		} else {
			e.backgroundWG.Add(1)
			go func() {
				defer e.backgroundWG.Done()

				tooLong := abfslog.NewLogIfTooLong(1*time.Minute, "WARNING: waiting for os.RemoveAll(%q) from req %q", absChrootDir, req.Hash)
				defer tooLong.Stop()

				// this will also stop the mount process from listening on chroot-*/.abfs.sock
				if err := os.RemoveAll(absChrootDir); err != nil {
					log.Printf("WARNING: Evaluator cannot remove chroot dir %q: %v", absChrootDir, err)
				}
			}()
		}
	}()

	tapDstPath := path.Join(absChrootDir, tapBinPath)
	dstFile, err := os.OpenFile(tapDstPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return nil, job, fmt.Errorf("cannot create file at %q: %w", tapDstPath, err)
	}
	if err := dstFile.Close(); err != nil {
		return nil, job, fmt.Errorf("cannot close file at %q: %w", tapDstPath, err)
	}

	if err := bindcas.CopyHashSize(e.tapPath, tapDstPath); err != nil {
		return nil, job, fmt.Errorf("cannot abfs copy tap binary from %q to %q: %w", e.tapPath, absChrootDir, err)
	}

	relToChrootSrcDir := "/src"

	absSrcDir := path.Join(absChrootDir, "src")

	if err := os.MkdirAll(absSrcDir, 0755); err != nil {
		return nil, job, fmt.Errorf("cannot create src dir %q: %w", absSrcDir, err)
	}

	envvars, err := e.fetchEnvVars(ctx, req.EnvVars, relToChrootSrcDir)
	if err != nil {
		return nil, job, err
	}

	relToMountSrcDir, err := filepath.Rel(e.mntPoint, absSrcDir)
	if err != nil {
		return nil, job, fmt.Errorf("cannot find relative path to %q from %q: %w", e.mntPoint, absSrcDir, err)
	}

	for _, p := range patches {
		if err := e.client.ApplyPatch(nil, p.Patch, nil, relToMountSrcDir); err != nil {
			return nil, job, fmt.Errorf("cannot apply patch %q: %w", h, err)
		}
	}

	if err := e.setupBasedOnEvalRequest(req, absSrcDir, relToMountSrcDir, envvars); err != nil {
		return nil, job, err
	}

	nsjailOpts, err := e.nsjailOpts(absChrootDir, relToChrootSrcDir, envvars)
	if err != nil {
		return nil, job, err
	}

	bCmd := run.BuildCommand(req.Command)
	updateStatus(JobStatusRun)

	tapCmd := append([]string{tapBinPath, "--prefix", relToMountSrcDir, "--"}, bCmd.Args...)
	// can use strings.Split("/bin/bash -c /bin/bash --noprofile --norc", " ")... as the command if you want to be dropped into an interactive shell to try out the environment
	output, err := nsjail.RunNSJailCaptureAll(ctx, path.Join(e.mntPoint, nsJailBin),
		path.Join(e.mntPoint, nsJailCfg), nsjailOpts, tapCmd...)
	if err != nil {
		return nil, job, fmt.Errorf("tap inside nsjail failed: output: %s, err: %w", output, err)
	}

	updateStatus(JobStatusPostProcess)
	var tapResult entities.TapCmdResult
	err = json.Unmarshal(output, &tapResult)
	if err != nil {
		return nil, job, fmt.Errorf("cannot parse json from tap output: %s, err: %w", output, err)
	}

	resp := binding.EvalRespFromTapResult(tapResult)
	resp.EvalRequest = req.Hash

	binding.ProcessResp(binding.EvalPostProcess{
		Resp:          resp,
		Req:           req,
		ChrootToStrip: relToMountSrcDir,
		CWD:           absSrcDir, //includes chroot prefix
	})

	if opt.WriteEvalResponse {
		if err := resp.Encode(); err != nil {
			return nil, job, fmt.Errorf("cannot encode the response: %w", err)
		}
		if err := e.store.PutObject(ctx, resp.Hash, resp.Encoded); err != nil {
			return nil, job, fmt.Errorf("cannot store the response in the store: %w", err)
		}
		if opt.StoreMapResult && resp.ExitCode == 0 {
			if err := e.store.AddMapResult(ctx, h, resp.Hash); err != nil {
				return nil, job, fmt.Errorf("cannot store %q as a map result of %q: %w", resp.Hash, h, err)
			}
		}
	}

	return resp, job, nil
}

func (e *Evaluator) ActiveJobs() []*ActiveJob {
	jobs := make([]*ActiveJob, 0)
	e.activeJobs.Range(func(key, value any) bool {
		if job, ok := value.(*ActiveJob); ok {
			jobs = append(jobs, job)
		}
		return true
	})
	return jobs
}

func (e *Evaluator) setupBasedOnEvalRequest(req *binding.EvalRequest, absSrcDir, relToMountSrcDir string, envvars []string) error {
	relOutDir := getEnv(envvars, entities.OutDirVar, "out")

	absOutDir := path.Join(absSrcDir, relOutDir)
	if err := os.MkdirAll(absOutDir, 0755); err != nil {
		return fmt.Errorf("cannot create out dir %q: %w", absOutDir, err)
	}

	for _, t := range req.Targets {
		if err := os.MkdirAll(path.Join(absSrcDir, path.Dir(t)), 0755); err != nil {
			return fmt.Errorf("cannot create target dir %q from target %q: %w", path.Dir(t), t, err)
		}
	}

	// Remove order-only inputs
	input := make(entities.Patch, 0, len(req.InputPatch))
	for _, p := range req.InputPatch {
		if !p.Hash.IsZero() {
			input = append(input, p)
		}
	}
	if err := e.client.ApplyPatch(nil, input, nil, relToMountSrcDir); err != nil {
		return fmt.Errorf("failed to apply the input patch: %w", err)
	}

	return nil
}

func (e *Evaluator) fetchEnvVars(ctx context.Context, hash entities.Hash, relSrcDir string) ([]string, error) {
	buf, err := e.store.GetObject(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("cannot fetch env-vars %s: %w", hash, err)
	}

	o := new(binding.EnvVars)
	if err := o.Decode(hash, buf); err != nil {
		return nil, err
	}

	return ninjago.DenormalizeEnvVars(o.Vars, relSrcDir), nil
}

func getEnv(envvars []string, key, defaultValue string) string {
	for _, kv := range envvars {
		if strings.HasPrefix(kv, key+"=") {
			return strings.TrimPrefix(kv, key+"=")
		}
	}
	return defaultValue
}

func mount(mntPoint string, fsType instance.CASFSType) (*exec.Cmd, error) {
	// We should try calling this as a go library instead of through exec so this abfs command can respect all config values of current abfs command.
	options := []string{"--fs", string(fsType), "casfs-mount", "--socket", entities.ABFSSocketFileName}
	options = append(options, mntPoint)
	mountCmd := exec.Command(os.Args[0], options...)
	mountCmd.Stdout = os.Stdout
	mountCmd.Stderr = os.Stderr
	if err := mountCmd.Start(); err != nil {
		return nil, fmt.Errorf("abfs mount failed to run '%s': %w", strings.Join(mountCmd.Args, " "), err)
	}
	return mountCmd, nil
}

func unmount(mntPoint string, mountCmd *exec.Cmd) error {
	log.Printf("Attempting to unmount. Kill the mount process (pid %d) if it takes too long", mountCmd.Process.Pid)
	// We should try calling this as a go library instead of through exec so this abfs command can respect all config values of current abfs command.
	umountCmd := exec.Command(os.Args[0], "umount", mntPoint, "--kill", "--no-mount-details")
	umountCmd.Stdout = os.Stdout
	umountCmd.Stderr = os.Stderr
	if err := umountCmd.Run(); err != nil {
		return fmt.Errorf("umount failed: %w", err)
	}
	if _, err := mountCmd.Process.Wait(); err != nil {
		return fmt.Errorf("while we were unmounting, the mount process exited with error: %w", err)
	}
	return nil
}

func copyExecutableFile(source, target string) (retErr error) {
	srcFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(srcFile, &retErr)

	if err := os.MkdirAll(path.Dir(target), 0755); err != nil {
		return err
	}

	dstFile, err := os.OpenFile(target, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(dstFile, &retErr)

	_, err = io.Copy(dstFile, srcFile)
	return err
}

func (e *Evaluator) nsjailOpts(absChrootDir, relSrcDir string, envvars []string) ([]string, error) {
	nsjailOpts := make([]string, 0, 2*len(envvars))
	for _, kv := range envvars {
		nsjailOpts = append(nsjailOpts, "--env", kv)
	}
	nsjailOpts = append(nsjailOpts, "-q")
	nsjailOpts = append(nsjailOpts, "--chroot", absChrootDir)
	nsjailOpts = append(nsjailOpts, "--disable_rlimits")

	// have to explicitly bind these so they're read/write
	nsjailOpts = append(nsjailOpts, "-B", fmt.Sprintf("%s:%s", path.Join(absChrootDir, relSrcDir), relSrcDir))
	nsjailOpts = append(nsjailOpts, "-B", fmt.Sprintf("%s:%s", path.Join(absChrootDir, binding.CtrlDir), binding.CtrlDir))

	nsjailOpts = append(nsjailOpts, "-R", "/lib")
	nsjailOpts = append(nsjailOpts, "-R", "/lib64")
	nsjailOpts = append(nsjailOpts, "-R", "/bin")
	nsjailOpts = append(nsjailOpts, "-R", "/sbin")
	nsjailOpts = append(nsjailOpts, "-R", "/usr")
	nsjailOpts = append(nsjailOpts, "-R", "/etc/alternatives")

	// this will overwrite the cwd value from the nsjail.cfg
	nsjailOpts = append(nsjailOpts, "--cwd", relSrcDir)

	return nsjailOpts, nil
}
