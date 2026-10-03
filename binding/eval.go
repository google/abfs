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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/google/abfs/binding/run"
	"github.com/google/abfs/maps"
	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const (
	ObjectTypeEnvVars ObjectType = "env-vars"

	// Bump the eval-request judiciously. It will result in 100% cache misses
	// until the cache is warmed again.
	CurrentEvalRequestVersion            = "0.9.2"
	ObjectTypeEvalRequest     ObjectType = "eval-request"

	CurrentEvalResponseVersion            = "0.7.5"
	ObjectTypeEvalResponse     ObjectType = "eval-response"

	inputPrefetchPriority = 0.5
)

type EnvVars struct {
	JSONBaseObject
	Vars map[string]string
}

func init() {
	objects.Factory.Register(ObjectTypeEnvVars, func() Object {
		return new(EnvVars)
	})
}

func (e *EnvVars) Encode() error {
	return ObjectJSONEncode(e, ObjectTypeEnvVars)
}

func (e *EnvVars) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(e, ObjectTypeEnvVars, hash, content)
}

func (e *EnvVars) Links() []Hash {
	// currently doesn't have any links to other objects
	return []Hash{}
}

type EvalRequest struct {
	JSONBaseObject
	Version string
	Salt    string `json:",omitempty"`
	Targets []string
	DepFile string `json:",omitempty"`
	Command string
	// Inputs is deprecated - use InputPatch
	Inputs map[string]Hash `json:",omitempty"`
	// The State field isn't used in the PatchEntry structs
	InputPatch entities.Patch
	RSP        string `json:",omitempty"`
	EnvVars    Hash
	// CWD - current working directory
	CWD string `json:",omitempty"`
}

func init() {
	objects.Factory.Register(ObjectTypeEvalRequest, func() Object {
		return new(EvalRequest)
	})
}

func (e *EvalRequest) Encode() error {
	sort.Strings(e.Targets)

	return ObjectJSONEncode(e, ObjectTypeEvalRequest)
}

func (e *EvalRequest) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(e, ObjectTypeEvalRequest, hash, content)
}

func (e *EvalRequest) Links() []Hash {
	links := make([]Hash, 0, 1+len(e.InputPatch))
	links = append(links, e.EnvVars)
	for _, ent := range e.InputPatch {
		if ent.Hash == entities.ZeroHash {
			continue
		}
		links = append(links, ent.Hash)
	}
	return links
}

func PrefetchInputPatch(p entities.Patch, cacheman *client.Client) error {
	blobs := make([]entities.Hash, 0)
	for _, entry := range p {
		if !entry.Hash.IsZero() && entry.Mode.IsFileOrSymlink() {
			blobs = append(blobs, entry.Hash)
		}
	}
	return cacheman.Prefetch(blobs, nil, inputPrefetchPriority)
}

var _ Object = (*EvalResponse)(nil)

type EvalResponse struct {
	JSONBaseObject
	Version     string
	EvalRequest Hash
	Stdout      string
	Stderr      string
	ExitCode    int
	RunInfo     struct {
		Pid                 int
		Timestamp           time.Time
		BuildStartHash      Hash
		RunDuration         float64
		SnapshotDuration    float64
		PostProcessDuration float64
		UserTime            float64
		SysTime             float64
		Memory              int64
		TapCounts           map[string]uint64 `json:",omitempty"`
		IO                  struct {
			In  int64
			Out int64
		}
		Misc struct {
			SoftFault           int64
			HardFault           int64
			VolContextSwitch    int64
			ForcedContextSwitch int64
		}
	}
	ReadPatch     entities.Patch
	MissingReads  map[string]entities.PatchEntryState `json:",omitempty"`
	WritePatch    entities.Patch
	MissingWrites map[string]entities.PatchEntryState `json:",omitempty"`
	Deletes       entities.DeletePatch
}

func init() {
	objects.Factory.Register(ObjectTypeEvalResponse, func() Object {
		return new(EvalResponse)
	})
}

func (e *EvalResponse) Encode() error {
	return ObjectJSONEncode(e, ObjectTypeEvalResponse)
}

func (e *EvalResponse) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(e, ObjectTypeEvalResponse, hash, content)
}
func (e *EvalResponse) CPUTime() float64 {
	return 100.0 * (float64(e.RunInfo.UserTime+e.RunInfo.SysTime) / e.RunInfo.RunDuration)
}

func (e *EvalResponse) Links() []Hash {
	links := make([]Hash, 0, 1+len(e.ReadPatch)+len(e.WritePatch))

	links = append(links, e.EvalRequest)

	for _, ent := range e.ReadPatch {
		if ent.Hash == entities.ZeroHash {
			continue
		}
		// The hash of directories in the Eval-Response's reads section is the dir-list-hash.
		// Currently we do not create an object out of the file-names of a dir and we just calculate
		// the hash which is going to be used for equality check in the Match() process.
		// We can't use the built-in FileMode.IsFile() because we set custom bits on the Patch.
		// TODO: Refactor Patch Mode to use custom type
		if ent.Mode&entities.FileModeDir != entities.FileModeDir {
			links = append(links, ent.Hash)
		}
	}

	for _, ent := range e.WritePatch {
		if ent.Hash == entities.ZeroHash {
			continue
		}
		links = append(links, ent.Hash)
	}

	return links
}

func (e *EvalResponse) Print(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "Version: %s\n", e.Version); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Eval Request: %s\n", e.EvalRequest); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Run info:\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Build Time: %s\tBuild Start: %s\n",
		e.RunInfo.Timestamp, e.RunInfo.BuildStartHash); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Times: Duration: %s\tSnapshot: %s\tPostProcess: %s\tUser: %s\tSystem: %s Cpu: %f%%\n",
		time.Duration(e.RunInfo.RunDuration*float64(time.Second)),
		time.Duration(e.RunInfo.SnapshotDuration*float64(time.Second)),
		time.Duration(e.RunInfo.PostProcessDuration*float64(time.Second)),
		time.Duration(e.RunInfo.UserTime*float64(time.Second)),
		time.Duration(e.RunInfo.SysTime*float64(time.Second)),
		e.CPUTime()); err != nil {
		return err
	}
	if len(e.RunInfo.TapCounts) > 0 {
		if _, err := fmt.Fprintf(w, "  Taps Received:\n"); err != nil {
			return err
		}
		keys := make([]string, 0, len(e.RunInfo.TapCounts))
		for k := range e.RunInfo.TapCounts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var totalTaps uint64
		for _, k := range keys {
			if _, err := fmt.Fprintf(w, "    %-20s: %d\n", k, e.RunInfo.TapCounts[k]); err != nil {
				return err
			}
			totalTaps += e.RunInfo.TapCounts[k]
		}
		if _, err := fmt.Fprintf(w, "\n    %-20s: %d\n", "Total Taps", totalTaps); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "  Usage Memory: %d\t\tIO in: %d\tIO out: %d\n",
		e.RunInfo.Memory, e.RunInfo.IO.In, e.RunInfo.IO.Out); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Exit Code: %d\n", e.ExitCode); err != nil {
		return err
	}
	if e.Stdout != "" {
		if _, err := fmt.Fprintln(w, "Stdout:"); err != nil {
			return err
		}
		for _, s := range strings.Split(e.Stdout, "\n") {
			if _, err := fmt.Fprintf(w, "| %s\n", s); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	if e.Stderr != "" {
		if _, err := fmt.Fprintln(w, "Stderr:"); err != nil {
			return err
		}
		for _, s := range strings.Split(e.Stderr, "\n") {
			if _, err := fmt.Fprintf(w, "| %s\n", s); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintln(w, "Reads:"); err != nil {
		return err
	}
	for _, p := range e.ReadPatch {
		if _, err := fmt.Fprintf(w, "%15s %s %s %12d %s\n", p.State, p.Hash, p.Mode, p.Size, p.Path); err != nil {
			return err
		}
	}
	for p, s := range e.MissingReads {
		if _, err := fmt.Fprintf(w, "%15s %s\n", s, p); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(w, "Writes:"); err != nil {
		return err
	}
	for _, p := range e.WritePatch {
		if _, err := fmt.Fprintf(w, "%15s %s %s %12d %s\n", p.State, p.Hash, p.Mode, p.Size, p.Path); err != nil {
			return err
		}
	}
	for p, s := range e.MissingWrites {
		if _, err := fmt.Fprintf(w, "%15s %s\n", s, p); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(w, "Deletes:"); err != nil {
		return err
	}
	for _, p := range e.Deletes {
		if _, err := fmt.Fprintf(w, "  %s\n", p); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	return nil
}

func DecodeEvalResponse(hash Hash, content []byte) (*EvalResponse, error) {
	r := new(EvalResponse)
	return r, r.Decode(hash, content)
}

// Filter a read patch, returning version of read patch and if it
// should be tried as part of apply
type ReadPatchFilter interface {
	Filter(patch entities.Patch, revalidate bool) (entities.Patch, error)
}
type ValidateInfo interface {
	ValidateError(resp *EvalResponse, err error)
}
type noopValidateInfo struct {
}

func (n *noopValidateInfo) ValidateError(resp *EvalResponse, err error) {
}

type Eval struct {
	Log              io.Writer
	client           *Client
	chroot           string
	req              *EvalRequest
	buildStart       *BuildStart
	Filter           ReadPatchFilter
	VI               ValidateInfo
	StrictTappingOff bool
}

func NewEval(abfs *Client, chroot string, strictTapping bool, req *EvalRequest, buildStart *BuildStart) *Eval {
	return &Eval{
		client:           abfs,
		chroot:           chroot,
		req:              req,
		Log:              os.Stdout,
		buildStart:       buildStart,
		Filter:           NewReadPatchFilter(),
		VI:               &noopValidateInfo{},
		StrictTappingOff: !strictTapping,
	}
}

func (e *Eval) MapRequest(proc *exec.Cmd) *maps.MapRequest {
	return &maps.MapRequest{
		Input:   e.req,
		MapFunc: e.Run(proc),
		OutputDecode: func(hash Hash, content []byte) (maps.MapObject, error) {
			return DecodeEvalResponse(hash, content)
		},
		Validator: e.Validate(),
		Store:     e.client.Store(),
	}
}

func (e *Eval) Validate() func(resp maps.MapObject) (bool, error) {
	return func(respObj maps.MapObject) (bool, error) {
		resp := respObj.(*EvalResponse)
		readPatch, err := e.Filter.Filter(resp.ReadPatch, false)
		if err == nil {
			err = e.client.ApplyPatch(readPatch, resp.WritePatch, resp.Deletes, e.chroot)
		}
		if err != nil {
			e.VI.ValidateError(resp, err)
			if IsMatchError(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil

	}
}

func (e *Eval) Run(proc *exec.Cmd) func(maps.MapObject) (maps.MapObject, error) {
	var tapPath string
	switch runtime.GOOS {
	case "linux":
		tapPath = "./prebuilts/build-tools/linux-x86/bin/tap"
	case "darwin":
		tapPath = "./prebuilts/build-tools/darwin-x86/bin/tap"
	}

	return func(maps.MapObject) (maps.MapObject, error) {

		// Run command through tap to record results
		var tapResult entities.TapCmdResult
		{
			tapOptions := []string{"--prefix", e.chroot, "--"}
			if !e.StrictTappingOff {
				tapOptions = append([]string{"--strict"}, tapOptions...)
			}

			tapCmd := exec.Command(tapPath, tapOptions...)
			tapCmd.Args = append(tapCmd.Args, proc.Args...)
			r := run.RunCommand(tapCmd)

			if r.Err != nil {
				return nil, r.Err
			}

			if err := json.Unmarshal([]byte(r.Stdout), &tapResult); err != nil {
				return nil, err
			}
		}

		resp := EvalRespFromTapResult(tapResult)
		if e.buildStart != nil {
			resp.RunInfo.BuildStartHash = e.buildStart.Hash
		}
		resp.EvalRequest = e.req.Hash

		ProcessResp(EvalPostProcess{
			Resp:          resp,
			Req:           e.req,
			ChrootToStrip: e.chroot,
		})

		if err := resp.Encode(); err != nil {
			return nil, err
		}

		if tapResult.ExitCode != 0 {
			return resp, fmt.Errorf("%s%s", tapResult.Stdout, tapResult.Stderr)
		}

		return resp, nil
	}
}

// EvalRespFromTapResult does a conversion of the raw tap data into an EvalResponse.
// ProcessResult will likely need to be called afterwards before use/storage.
func EvalRespFromTapResult(tap entities.TapCmdResult) *EvalResponse {
	resp := ResponseFromRun(run.RunResult{
		Pid:         tap.Pid,
		ExitCode:    tap.ExitCode,
		Rusage:      tap.Rusage,
		Timestamp:   tap.Timestamp,
		RunDuration: tap.RunDuration,
		Stdout:      []byte(tap.Stdout),
		Stderr:      tap.Stderr,
	})

	resp.ReadPatch = tap.ReadPatch
	resp.WritePatch = tap.WritePatch
	resp.Deletes = tap.Deletes
	resp.RunInfo.TapCounts = tap.TapCounts
	resp.RunInfo.SnapshotDuration = tap.SnapshotDuration

	return resp
}

// Revalidate if a output hash is still valid based on the current work directory
func Revalidate(ctx context.Context, abfs *Client, chroot string, outHash entities.Hash, filter ReadPatchFilter) (*EvalResponse, error) {
	outBytes, err := abfs.Store().GetObject(ctx, outHash)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch object %s: %w", outHash, err)
	}
	resp, err := DecodeEvalResponse(outHash, outBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to decode object %s: %w", outHash, err)
	}

	patches := append(resp.ReadPatch, resp.WritePatch...)
	if filter != nil {
		patches, err = filter.Filter(patches, true)
	}

	if err == nil {
		// Send patch with empty write patch and deletes
		noDirPatch := make(entities.Patch, 0, len(patches))
		for _, p := range patches {
			if strings.Contains(p.Path, "/soong/metrics_aggregation/") { // Soong removes all the metrics on a rerun
				continue
			}
			if p.Mode&entities.FileModeDir == entities.FileModeDir { // FIXME bypass dir match for revalidate
				continue
			}
			noDirPatch = append(noDirPatch, p)
		}
		err = abfs.ApplyPatch(noDirPatch, nil, nil, chroot)
	}
	if IsMatchError(err) {
		return nil, nil
	}
	if err != nil {

		return nil, err
	}
	return resp, nil
}

func DiffRunInfo(out io.Writer, r1, r2 *EvalResponse) {
	_, _ = fmt.Fprintf(out, "%24s\tt1:%s\tt2:%s\t%s\n", "", r1.Hash, r2.Hash, "t2-t1")
	writeFloat := func(label string, f1, f2 float64) {
		_, _ = fmt.Fprintf(out, "%24s\t%43f\t%43f\t%f\n", label, f1, f2, f2-f1)
	}
	writeTime := func(label string, t1, t2 time.Time) {
		_, _ = fmt.Fprintf(out, "%24s\t%43s\t%43s\t%s\n", label, t1, t2, t2.Sub(t1))
	}
	writeInt64 := func(label string, i1, i2 int64) {
		_, _ = fmt.Fprintf(out, "%24s\t%43d\t%43d\t%d\n", label, i1, i2, i2-i1)
	}
	writeTime("timestamp", r1.RunInfo.Timestamp, r2.RunInfo.Timestamp)
	writeFloat("cpu time %", r1.CPUTime(), r2.CPUTime())
	writeFloat("run duration", r1.RunInfo.RunDuration, r2.RunInfo.RunDuration)
	writeFloat("user time", r1.RunInfo.UserTime, r2.RunInfo.UserTime)
	writeFloat("sys time", r1.RunInfo.SysTime, r2.RunInfo.SysTime)
	writeInt64("memory", r1.RunInfo.Memory, r2.RunInfo.Memory)
	writeInt64("IO.in", r1.RunInfo.IO.In, r2.RunInfo.IO.In)
	writeInt64("IO.out", r1.RunInfo.IO.Out, r2.RunInfo.IO.Out)
	writeInt64("Soft Fault", r1.RunInfo.Misc.SoftFault, r2.RunInfo.Misc.SoftFault)
	writeInt64("Hard Fault", r1.RunInfo.Misc.HardFault, r2.RunInfo.Misc.HardFault)
	writeInt64("Vol Ctx", r1.RunInfo.Misc.VolContextSwitch, r2.RunInfo.Misc.VolContextSwitch)
	writeInt64("Forced Ctx", r1.RunInfo.Misc.ForcedContextSwitch, r2.RunInfo.Misc.ForcedContextSwitch)
}
