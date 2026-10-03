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

package binding

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/abfs/binding/run"
	"github.com/google/abfs/shared/entities"
)

// ProcessResp strips any chroot prefix off of the eval response paths and labels
// the inputs and outputs with the appropriate expected vs. unexpected labels.
func ProcessResp(e EvalPostProcess) {
	start := time.Now()
	e.processResp()
	e.Resp.RunInfo.PostProcessDuration = time.Since(start).Seconds()
}

type EvalPostProcess struct {
	Resp *EvalResponse
	// ChrootToStrip is the chroot prefix to strip off of the eval response paths.
	// It is not joined with the CWD for path resolution. Append the chroot to CWD value if this is necessary.
	ChrootToStrip string
	Req           *EvalRequest
	// CWD is optional if we are already os.Chdir'd to the correct directory.
	// If specified, it is joined with any relative paths that
	// need to be opened like DepFile and symlinks. It is not joined with
	// ChrootToStrip.
	CWD string
}

func (e *EvalPostProcess) processResp() {
	e.Resp.ReadPatch.ChrootTransform(e.ChrootToStrip)
	e.Resp.WritePatch.ChrootTransform(e.ChrootToStrip)
	e.Resp.Deletes.ChrootTransform(e.ChrootToStrip)

	e.processRespWrites()

	writeDirs := map[string]struct{}{}
	var addDir func(p string)
	addDir = func(p string) {
		if _, ok := writeDirs[p]; !ok {
			writeDirs[p] = struct{}{}
			addDir(filepath.Dir(p))
		}
	}
	for _, p := range e.Resp.WritePatch {
		addDir(filepath.Dir(p.Path))
	}

	var deps map[string]struct{}
	if e.Req.DepFile != "" {
		deps = make(map[string]struct{})
		depFile := e.Req.DepFile
		if !filepath.IsAbs(depFile) {
			depFile = filepath.Join(e.CWD, depFile)
		}
		if err := e.addDeps(depFile, deps); err != nil && e.Resp.ExitCode == 0 {
			cwd, _ := os.Getwd()
			fmt.Fprintf(os.Stderr, "cannot read dep-file using path %q with PWD %q: %s\n", depFile, cwd, err)
		}
	}

	e.processRespReads(deps, writeDirs)
}

func (e *EvalPostProcess) processRespWrites() {
	output := make(map[string]struct{}, len(e.Req.Targets)+1)
	for _, i := range e.Req.Targets {
		// Tapping operates on relative paths.
		// Strip root directory if it is present.
		cut := strings.TrimPrefix(i, entities.SrcDir+"/")
		output[cut] = struct{}{}
	}
	depFile := e.Req.DepFile
	if depFile != "" {
		output[depFile] = struct{}{}
	}

	for _, p := range e.Resp.WritePatch {
		processWritePatch(p, output, &depFile)
	}

	// resolve symlinks only after initial process for performance reasons
	e.resolveSymlinks(output)
	if depFile != "" {
		depFile = e.resolveSymlink(depFile)
	}

	for _, p := range e.Resp.WritePatch {
		if p.State == entities.PatchEntryStateExtraWrite {
			processWritePatch(p, output, &depFile)
		} else {
			delete(output, p.Path)
		}
	}

	if len(output) > 0 || depFile != "" {
		e.Resp.MissingWrites = make(map[string]entities.PatchEntryState, len(output)+1)
	}
	for s := range output {
		e.Resp.MissingWrites[s] = entities.PatchEntryStateMissingOut
	}
	if depFile != "" {
		e.Resp.MissingWrites[depFile] = entities.PatchEntryStateMissingDepFile
	}
}

func processWritePatch(p *entities.PatchEntry, output map[string]struct{}, depFile *string) {
	if p.Mode == entities.FileModeDir {
		p.SetState(entities.PatchEntryStateDir)
		return
	}
	if _, ok := output[p.Path]; ok {
		if *depFile != "" && p.Path == *depFile {
			p.SetState(entities.PatchEntryStateDepFile)
			*depFile = ""
		} else {
			p.SetState(entities.PatchEntryStateOut)
		}
		delete(output, p.Path)
	} else {
		p.SetState(entities.PatchEntryStateExtraWrite)
	}
}

func (e *EvalPostProcess) processRespReads(deps map[string]struct{}, writeDirs map[string]struct{}) {
	input := make(map[string]struct{}, len(e.Req.InputPatch))
	orderOnlies := make(map[string]struct{}, 0)
	for _, in := range e.Req.InputPatch {
		// Tapping operates on relative paths.
		// Strip root directory if it is present.
		cut := strings.TrimPrefix(in.Path, entities.SrcDir+"/")
		if in.State == entities.PatchEntryStateOrderOnly {
			orderOnlies[cut] = struct{}{}
		} else {
			input[cut] = struct{}{}
		}
	}
	rsp := e.Req.RSP

	for _, p := range e.Resp.ReadPatch {
		processReadPatch(p, input, orderOnlies, &rsp, deps, writeDirs)
	}

	// resolve symlinks only after initial process for performance reasons
	e.resolveSymlinks(input)
	e.resolveSymlinks(orderOnlies)
	e.resolveSymlinks(deps)
	if rsp != "" {
		rsp = e.resolveSymlink(rsp)
	}

	for _, p := range e.Resp.ReadPatch {
		if p.State == entities.PatchEntryStateExtraRead {
			processReadPatch(p, input, orderOnlies, &rsp, deps, writeDirs)
		} else {
			delete(input, p.Path)
			delete(orderOnlies, p.Path)
			delete(deps, p.Path)
		}
	}

	if len(input) > 0 || len(orderOnlies) > 0 || rsp != "" || len(deps) > 0 {
		e.Resp.MissingReads = make(map[string]entities.PatchEntryState, len(input)+len(orderOnlies)+len(deps)+1)
	}
	for s := range input {
		e.Resp.MissingReads[s] = entities.PatchEntryStateExtraIn
	}
	for s := range orderOnlies {
		e.Resp.MissingReads[s] = entities.PatchEntryStateNotReadOrderOnly
	}
	for s := range deps {
		e.Resp.MissingReads[s] = entities.PatchEntryStateNotReadDep
	}
	if rsp != "" {
		e.Resp.MissingReads[rsp] = entities.PatchEntryStateNotReadRSP
	}
}

func processReadPatch(p *entities.PatchEntry, input, orderOnlies map[string]struct{}, rsp *string, deps, writeDirs map[string]struct{}) {
	if p.Mode == entities.FileModeDir {
		if _, ok := writeDirs[p.Path]; ok {
			p.SetState(entities.PatchEntryStateDirModified)
		} else {
			p.SetState(entities.PatchEntryStateDir)
		}
		return
	}
	if _, ok := input[p.Path]; ok {
		if *rsp != "" && p.Path == *rsp {
			p.SetState(entities.PatchEntryStateRSP)
			*rsp = ""
		} else {
			p.SetState(entities.PatchEntryStateIn)
		}
		delete(input, p.Path)
		delete(deps, p.Path)
	} else if _, ok := deps[p.Path]; ok {
		p.SetState(entities.PatchEntryStateDep)
		delete(deps, p.Path)
		delete(orderOnlies, p.Path)
	} else if _, ok := orderOnlies[p.Path]; ok {
		p.SetState(entities.PatchEntryStateOrderOnly)
		delete(orderOnlies, p.Path)
	} else {
		p.SetState(entities.PatchEntryStateExtraRead)
	}
}

func (e *EvalPostProcess) addDeps(depFile string, deps map[string]struct{}) (retErr error) {
	file, err := os.Open(depFile)
	if err != nil {
		return err
	}
	defer func() { retErr = entities.FirstError(retErr, file.Close()) }()

	// FIXME this is a terrible way to parse a dep file!
	reader := bufio.NewReader(file)
	if _, err := reader.ReadBytes(':'); err != nil {
		return err
	}
	scanner := bufio.NewScanner(reader)
	scanner.Split(bufio.ScanWords)
	for scanner.Scan() {
		if scanner.Text() == "\\" {
			continue
		}
		deps[scanner.Text()] = struct{}{}
	}
	return scanner.Err()
}

func (e *EvalPostProcess) resolveSymlink(s string) string {
	startPath := s
	if !filepath.IsAbs(s) {
		startPath = filepath.Join(e.CWD, s)
	}

	r, err := filepath.EvalSymlinks(startPath)
	if err != nil { // It is best effort, ignore the error and return original path.
		return s
	}

	if e.CWD != "" {
		if rel, err := filepath.Rel(e.CWD, r); err == nil {
			r = rel
		}
	}

	return entities.ChrootTransform(r, e.ChrootToStrip)
}

func (e *EvalPostProcess) resolveSymlinks(files map[string]struct{}) {
	for s := range files {
		s2 := e.resolveSymlink(s)
		if s != s2 {
			delete(files, s)
			files[s2] = struct{}{}
		}
	}
}

func ResponseFromRun(r run.RunResult) *EvalResponse {
	resp := &EvalResponse{
		Version:  CurrentEvalResponseVersion,
		Stdout:   string(r.Stdout),
		Stderr:   r.Stderr,
		ExitCode: r.ExitCode,
	}
	resp.RunInfo.Timestamp = r.Timestamp
	resp.RunInfo.RunDuration = r.RunDuration
	resp.RunInfo.Pid = r.Pid
	if r.Rusage != nil {
		resp.RunInfo.UserTime = float64(r.Rusage.Utime.Sec) + time.Microsecond.Seconds()*float64(r.Rusage.Utime.Usec)
		resp.RunInfo.SysTime = float64(r.Rusage.Stime.Sec) + time.Microsecond.Seconds()*float64(r.Rusage.Stime.Usec)
		resp.RunInfo.Memory = r.Rusage.Maxrss
		resp.RunInfo.IO.In = r.Rusage.Inblock
		resp.RunInfo.IO.Out = r.Rusage.Oublock
		resp.RunInfo.Misc.SoftFault = r.Rusage.Minflt
		resp.RunInfo.Misc.HardFault = r.Rusage.Majflt
		resp.RunInfo.Misc.VolContextSwitch = r.Rusage.Nvcsw
		resp.RunInfo.Misc.ForcedContextSwitch = r.Rusage.Nivcsw
	}
	return resp
}
