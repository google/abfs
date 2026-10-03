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
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/maps"
	"github.com/google/abfs/ninjago"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/version"
	"github.com/google/abfs/store"
)

type Hash = entities.Hash

var allowedVars = []string{
	"DIST_DIR",
	"HOME",
	"PATH",
	"PWD",
}

type cmd struct {
	logFile     string
	inputPaths  []string
	outputPaths []string
	command     []string

	cacheDir     string
	localOnly    bool
	fetchTimeout time.Duration

	force   bool
	verbose bool
	dryRun  bool
}

type cmdout struct {
	file *os.File
	data *bytes.Buffer
}

func newCmdOut(f *os.File) *cmdout {
	return &cmdout{file: f, data: new(bytes.Buffer)}

}

func (o *cmdout) Write(p []byte) (n int, err error) {
	_, err = o.data.Write(p)
	if err != nil {
		return 0, err
	}
	return o.data.Write(p)
}

func main() {
	c := &cmd{}
	pflag.StringVar(&c.logFile, "log", entities.OutDir+"/.cmd_map_log", "The path to log file")

	pflag.StringArrayVar(&c.inputPaths, "input-path", nil, "The file or directory which its hash will be used as an input in the cache request")
	pflag.StringArrayVar(&c.outputPaths, "output-path", nil, "The file or directory which its hash will be used as an output in the cache response")

	pflag.StringVarP(&c.cacheDir, "cache-dir", "c", "", "ABFS cache directory")
	pflag.BoolVarP(&c.localOnly, "local-only", "l", false, "Run locally without remote access")
	pflag.DurationVar(&c.fetchTimeout, "fetch-timeout", 2*time.Minute, "The timeout for fetching remote objects")

	pflag.BoolVarP(&c.verbose, "verbose", "v", false, "Run in verbose mode")
	pflag.BoolVarP(&c.force, "force", "f", false, "Force run command even if cache matches")
	pflag.BoolVarP(&c.dryRun, "dry-run", "", false, "Dry run to see if command would have been a cache hit or miss")
	showVersion := pflag.Bool("version", false, "Print the application version")
	pflag.Parse()

	if *showVersion {
		fmt.Println(version.Version())
		return
	}

	c.command = pflag.Args()

	if len(c.command) == 0 {
		fmt.Fprintf(os.Stderr, "no commands provided\n")
		pflag.Usage()
		os.Exit(1)
	}

	// Need a store
	casStore, err := store.NewCASStore(c.cacheDir, c.localOnly, c.fetchTimeout)
	if err != nil {
		log.Fatal("failed to access cache store")
	}

	// Read the input
	input, err := fetchInputs(c.inputPaths)
	if err != nil {
		log.Fatal(err)
	}

	// For outputs insure all paths are empty
	for _, p := range c.outputPaths {
		if err := os.MkdirAll(p, 0755); err != nil {
			log.Fatalf("failed to mkdir %s %v", p, err)
		}
	}
	// If dry run try not to modify the tree
	if !c.dryRun {
		outputHashes, err := fetchHashes(c.outputPaths)
		if err != nil {
			log.Fatal("failed to fetch output paths")
		}

		for p, hash := range outputHashes {
			if hash != entities.EmptyDirHash {
				log.Fatalf("output directory %s is not empty", p)
			}
		}
	}

	ret, err := c.process(casStore, input)
	if err != nil {
		log.Fatal(err)
	}
	os.Exit(ret)
}
func fetchHashes(paths []string) (map[string]Hash, error) {
	result := make(map[string]Hash, len(paths))
	for _, p := range paths {
		h, err := bindcas.Hash(p)
		if err != nil {
			return nil, err
		}
		result[p] = h
	}

	return result, nil
}

func fetchInputs(paths []string) (entities.Patch, error) {
	result := make(entities.Patch, 0, len(paths))
	for _, p := range paths {
		h, err := bindcas.HashStat(p)
		if err != nil {
			return nil, err
		}
		result = append(result, &entities.PatchEntry{
			Path: p,
			Hash: h.Hash,
			Mode: h.Mode,
			Size: h.Size,
		})
	}

	return result, nil
}

func (c *cmd) writePatch() (entities.Patch, error) {

	var out entities.Patch
	for _, p := range c.outputPaths {
		h, err := bindcas.Hash(p)
		if err != nil {
			return nil, err
		}
		out = append(out, &entities.PatchEntry{Path: p, Mode: entities.FileModeDir,
			Hash: h, Size: 0, State: entities.PatchEntryStateDir})
	}
	return out, nil
}

func (c *cmd) Apply(respObj maps.MapObject) (bool, error) {
	if c.dryRun {
		return true, nil
	}
	resp := respObj.(*binding.EvalResponse)
	for _, w := range resp.WritePatch {
		if w.Mode != entities.FileModeDir {
			return false, fmt.Errorf("patch contains non directory entry %s", w.Path)
		}
		if c.verbose {
			fmt.Printf("Creating path %s with hash %s\n", w.Path, w.Hash)
		}
		if err := os.MkdirAll(w.Path, 0755); err != nil {
			return false, fmt.Errorf("failed to mkdir %s %w", w.Path, err)
		}
		if err := bindcas.SetHash(w.Path, w.Hash); err != nil {
			return false, fmt.Errorf("failed to set hash of dir %s to %s %w", w.Path, w.Hash, err)
		}
	}
	return true, nil

}
func (c *cmd) MapFunc(proc *exec.Cmd) func(maps.MapObject) (maps.MapObject, error) {

	return func(input maps.MapObject) (maps.MapObject, error) {
		// Run build and return the hash
		execStart := time.Now()

		// Run the command if successful build eval repsonse data
		// Capture stdout/stderr and also print as normal
		stdoutW := newCmdOut(os.Stdout)
		stderrW := newCmdOut(os.Stderr)
		proc.Stdout = stdoutW
		proc.Stderr = stderrW

		err := proc.Run()
		if err != nil {
			err = fmt.Errorf("failed to exec command: %v", err)
			return nil, err
		}

		wp, err := c.writePatch()
		if err != nil {
			return nil, err
		}
		resp := &binding.EvalResponse{
			Version:    binding.CurrentEvalResponseVersion,
			ExitCode:   proc.ProcessState.ExitCode(),
			WritePatch: wp,
			Stdout:     stdoutW.data.String(),
			Stderr:     stderrW.data.String(),
		}
		rusage := proc.ProcessState.SysUsage().(*syscall.Rusage)

		resp.EvalRequest = input.GetHash()

		resp.RunInfo.Pid = proc.Process.Pid
		resp.RunInfo.Timestamp = execStart
		resp.RunInfo.RunDuration = time.Since(execStart).Seconds()
		resp.RunInfo.UserTime = float64(rusage.Utime.Sec) + time.Microsecond.Seconds()*float64(rusage.Utime.Usec)
		resp.RunInfo.SysTime = float64(rusage.Stime.Sec) + time.Microsecond.Seconds()*float64(rusage.Stime.Usec)
		resp.RunInfo.Memory = rusage.Maxrss
		resp.RunInfo.IO.In = rusage.Inblock
		resp.RunInfo.IO.Out = rusage.Oublock
		resp.RunInfo.Misc.SoftFault = rusage.Minflt
		resp.RunInfo.Misc.HardFault = rusage.Majflt
		resp.RunInfo.Misc.VolContextSwitch = rusage.Nvcsw
		resp.RunInfo.Misc.ForcedContextSwitch = rusage.Nivcsw
		return resp, nil

	}
}

func (c *cmd) process(casStore entities.CASStore, input entities.Patch) (int, error) {
	allowedSet := make(map[string]struct{}, len(allowedVars))
	for _, k := range allowedVars {
		allowedSet[k] = struct{}{}
	}
	cwd, _ := os.Getwd()
	normalEnv := ninjago.NormalizeEnvVars(os.Environ(), cwd, func(key string) bool {
		_, ok := allowedSet[key]
		return ok
	})

	var filteredEnv []string
	for _, kv := range os.Environ() {
		ss := strings.SplitN(kv, "=", 2)
		if len(ss) >= 2 {
			if _, ok := allowedSet[ss[0]]; ok {
				filteredEnv = append(filteredEnv, kv)
			}
		}
	}
	envVarsHash, err := binding.StoreEnvVars(casStore, normalEnv)
	if err != nil {
		return 0, err
	}
	if c.verbose {
		fmt.Printf("Running with env hash %s\n", envVarsHash)
	}
	cmd := exec.Command(c.command[0], c.command[1:]...)
	cmd.Env = filteredEnv

	req := &binding.EvalRequest{
		Version: binding.CurrentEvalRequestVersion,
		//FIXME Add salt from ninja-config
		Targets:    []string{"'" + strings.Join(c.command, " ") + "'"},
		Command:    strings.Join(c.command, " "),
		InputPatch: input,
		EnvVars:    envVarsHash,
	}

	mr := &maps.MapRequest{
		Input:   req,
		MapFunc: c.MapFunc(cmd),
		OutputDecode: func(hash entities.Hash, content []byte) (maps.MapObject, error) {
			return binding.DecodeEvalResponse(hash, content)
		},
		Validator: c.Apply,
		Store:     casStore,
	}

	respObj, err := c.runMap(context.Background(), mr)
	if err != nil {
		return 0, err
	}
	if c.dryRun {
		if respObj != nil {
			resp := respObj.(*binding.EvalResponse)
			fmt.Printf("cache-hit: %s %s\n", req.Hash, resp.Hash)
		} else {
			fmt.Printf("cache-miss: %s\n", req.Hash)
		}
		return 0, nil
	}
	resp := respObj.(*binding.EvalResponse)
	if resp != nil {
		if mr.CacheHit {
			fmt.Printf("cache-hit: %s %s\n", req.Hash, resp.Hash)
		} else {
			fmt.Printf("cache-miss: %s\n", req.Hash)
		}
		if err := c.log(mr, req, resp); err != nil {
			fmt.Fprintf(os.Stderr, "could not log req=%s resp=%s: %v", req.Hash, resp.Hash, err)
		}

		return resp.ExitCode, nil
	}
	return -1, err
}
func (c *cmd) runMap(ctx context.Context, req *maps.MapRequest) (o maps.MapObject, err error) {
	inHash, err := maps.MapInput(ctx, req)
	if err != nil {
		return nil, err
	}
	out, err := maps.MapExisting(ctx, inHash, req)
	if err != nil {
		return nil, err
	}
	if out != nil {
		if !c.force {
			return out, nil
		}
		fmt.Printf("force rebuild of existing result\n")
	}
	if c.dryRun {
		return out, nil
	}

	return maps.MapExecute(ctx, inHash, req)
}

func (c *cmd) log(mapReq *maps.MapRequest, req *binding.EvalRequest, resp *binding.EvalResponse) (retErr error) {
	file, err := os.OpenFile(c.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer func() { retErr = entities.FirstError(retErr, file.Close()) }()
	_, err = fmt.Fprintf(file, "%-13d %8.3f %-6t %-40s %-40s %3d %s\n",
		mapReq.StartTime.UnixMilli(),
		mapReq.Duration.Seconds(),
		mapReq.CacheHit,
		req.Hash,
		resp.Hash,
		mapReq.InvalidCount,
		strings.Join(c.command, " "),
	)
	return err
}
