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

package bazeldaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"golang.org/x/sync/errgroup"
	"google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	re "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	sv "github.com/bazelbuild/remote-apis/build/bazel/semver"
	"github.com/google/uuid"
	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/store"
	outputservice "github.com/google/abfs/third_party/bazel/src/main/protobuf"
)

const maxNumberOfDirs = 1000000

type Server struct {
	outputservice.UnimplementedBazelOutputServiceServer

	lock        sync.Mutex
	store       entities.CASStore
	client      *binding.Client
	root        string
	buildOut    string
	builds      map[string]Build
	nsjail      bool
	forceRun    bool
	directories *lru.Cache[string, *re.Directory]
}

type Build struct {
	Start        time.Time
	Duration     time.Duration
	BuildID      string
	OutputBaseID string
	OutputPath   string
}

const outputPathSuffix = "casfs-store"

type Span struct {
	tag   string
	start time.Time
	end   time.Time
}

func NewSpan(format string, v ...any) *Span {
	tag := fmt.Sprintf(format, v...)
	log.Printf("Call %s", tag)

	return &Span{
		tag:   tag,
		start: time.Now(),
	}
}

func (s *Span) End() {
	s.end = time.Now()

	log.Printf("Called(%s) %s", s.end.Sub(s.start), s.tag)
}

// StartBuild implements outputservice.BazelOutputServiceServer.
func (s *Server) StartBuild(ctx context.Context, req *outputservice.StartBuildRequest) (*outputservice.StartBuildResponse, error) {
	defer NewSpan("StartBuild(BuildId=%s, OutputBaseId=%s, OutputPathPrefix=%q)", req.BuildId, req.OutputBaseId, req.OutputPathPrefix).End()

	s.lock.Lock()
	defer s.lock.Unlock()

	if _, ok := s.builds[req.BuildId]; ok {
		return nil, fmt.Errorf("build with id %s already exists", req.BuildId)
	}
	build := Build{
		Start:        time.Now(),
		BuildID:      req.BuildId,
		OutputBaseID: req.OutputBaseId,
		OutputPath:   path.Join(req.OutputPathPrefix, outputPathSuffix),
	}

	if err := os.MkdirAll(build.OutputPath, 0755); err != nil {
		return nil, err
	}

	s.builds[req.BuildId] = build

	return &outputservice.StartBuildResponse{
		OutputPathSuffix: outputPathSuffix,
	}, nil
}

// BatchStat implements outputservice.BazelOutputServiceServer.
func (s *Server) BatchStat(ctx context.Context, req *outputservice.BatchStatRequest) (*outputservice.BatchStatResponse, error) {
	defer NewSpan("BatchStat(BuildId=%s, Paths=%s)", req.BuildId, req.Paths).End()

	s.lock.Lock()
	build, ok := s.builds[req.BuildId]
	s.lock.Unlock()
	if !ok {
		return nil, fmt.Errorf("build with id %s does not exists", req.BuildId)
	}

	resp := &outputservice.BatchStatResponse{
		Responses: make([]*outputservice.BatchStatResponse_StatResponse, len(req.Paths)),
	}
	for i, p := range req.Paths {
		resp.Responses[i] = &outputservice.BatchStatResponse_StatResponse{}
		p = path.Join(build.OutputPath, p)
		stat, err := os.Lstat(p)
		if entities.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		resp.Responses[i].Stat = &outputservice.BatchStatResponse_Stat{}

		switch {
		case stat.IsDir():
			resp.Responses[i].Stat.Type = &outputservice.BatchStatResponse_Stat_Directory_{
				Directory: &outputservice.BatchStatResponse_Stat_Directory{},
			}
		case stat.Mode().IsRegular():
			hash, err := bindcas.Hash(p)
			if err != nil {
				return nil, fmt.Errorf("failed to read file hash %s: %w", p, err)
			}

			loc := &outputservice.FileArtifactLocator{
				Digest: &re.Digest{
					Hash:      hash.String(),
					SizeBytes: stat.Size(),
				},
			}
			pbLoc, err := anypb.New(loc)
			if err != nil {
				return nil, fmt.Errorf("failed to create Locator pb %s: %w", p, err)
			}
			resp.Responses[i].Stat.Type = &outputservice.BatchStatResponse_Stat_File_{
				File: &outputservice.BatchStatResponse_Stat_File{
					Locator: pbLoc,
				},
			}
		case stat.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return nil, err
			}
			resp.Responses[i].Stat.Type = &outputservice.BatchStatResponse_Stat_Symlink_{
				Symlink: &outputservice.BatchStatResponse_Stat_Symlink{
					Target: target,
				},
			}
		default:
			return nil, fmt.Errorf("unsupported file type %s", p)
		}
	}

	return resp, nil
}

// Clean implements outputservice.BazelOutputServiceServer.
func (s *Server) Clean(ctx context.Context, req *outputservice.CleanRequest) (*outputservice.CleanResponse, error) {
	defer NewSpan("Clean(OutputBaseId=%v)", req.OutputBaseId).End()

	return &outputservice.CleanResponse{}, nil
}

// FinalizeArtifacts implements outputservice.BazelOutputServiceServer.
func (s *Server) FinalizeArtifacts(ctx context.Context, req *outputservice.FinalizeArtifactsRequest) (*outputservice.FinalizeArtifactsResponse, error) {
	defer NewSpan("FinalizeArtifacts(BuildId=%s, Artifacts=%d)", req.BuildId, len(req.Artifacts)).End()

	return &outputservice.FinalizeArtifactsResponse{}, nil
}

// FinalizeBuild implements outputservice.BazelOutputServiceServer.
func (s *Server) FinalizeBuild(ctx context.Context, req *outputservice.FinalizeBuildRequest) (*outputservice.FinalizeBuildResponse, error) {
	defer NewSpan("FinalizeBuild(BuildId=%s, Successful=%v)", req.BuildId, req.BuildSuccessful).End()

	s.lock.Lock()
	defer s.lock.Unlock()
	build, ok := s.builds[req.BuildId]
	if !ok {
		return nil, fmt.Errorf("build with id %s does not exists", req.BuildId)
	}
	delete(s.builds, req.BuildId)

	var status string
	if req.BuildSuccessful {
		status = "SUCCESS"
	} else {
		status = "FAILURE"
	}

	log.Printf("Build BuildId=%s finished %s in %s", req.BuildId, status, time.Since(build.Start))
	return &outputservice.FinalizeBuildResponse{}, nil
}

func GetArtifactLocator(pbLocator *anypb.Any) (*outputservice.FileArtifactLocator, error) {
	locator := &outputservice.FileArtifactLocator{}
	if err := pbLocator.UnmarshalTo(locator); err != nil {
		return nil, fmt.Errorf("failed to unmarshal locator: %w", err)
	}
	return locator, nil
}

// StageArtifacts implements outputservice.BazelOutputServiceServer.
func (s *Server) StageArtifacts(ctx context.Context, req *outputservice.StageArtifactsRequest) (*outputservice.StageArtifactsResponse, error) {
	defer NewSpan("StageArtifacts(BuildId=%s, Files=%d)", req.BuildId, len(req.Artifacts)).End()

	s.lock.Lock()
	build, ok := s.builds[req.BuildId]
	s.lock.Unlock()
	if !ok {
		return nil, fmt.Errorf("build with id %s does not exists", req.BuildId)
	}

	resp := &outputservice.StageArtifactsResponse{
		Responses: make([]*outputservice.StageArtifactsResponse_Response, len(req.Artifacts)),
	}

	for i, art := range req.Artifacts {
		log.Printf("staging %s %s", art.Path, art.Locator)
		loc, err := GetArtifactLocator(art.Locator)
		if err != nil {
			return nil, err
		}

		p := path.Join(build.OutputPath, art.Path)
		if err := os.MkdirAll(path.Dir(p), 0755); err != nil {
			return nil, err
		}

		if err := os.WriteFile(p, nil, 0644); err != nil {
			return nil, err
		}
		if err := bindcas.SetHashSize(p, entities.NewHash(loc.Digest.Hash), loc.Digest.SizeBytes); err != nil {
			return nil, err
		}
		resp.Responses[i] = &outputservice.StageArtifactsResponse_Response{}
	}

	return resp, nil
}

func NewServer(store entities.CASStore, client *binding.Client, root, buildOut string, nsjail bool, forceRun bool) *Server {
	dirs, err := lru.New[string, *re.Directory](maxNumberOfDirs)
	if err != nil { // never happens as the lru.New() only fails on negative sizes.
		panic(err)
	}

	return &Server{
		store:       store,
		client:      client,
		root:        root,
		buildOut:    buildOut,
		builds:      make(map[string]Build),
		nsjail:      nsjail,
		forceRun:    forceRun,
		directories: dirs,
	}
}

func (s *Server) Register(grpcServer *grpc.Server) {
	re.RegisterExecutionServer(grpcServer, s)
	re.RegisterCapabilitiesServer(grpcServer, s)
	re.RegisterActionCacheServer(grpcServer, s)
	re.RegisterContentAddressableStorageServer(grpcServer, s)
	bytestream.RegisterByteStreamServer(grpcServer, s)
	outputservice.RegisterBazelOutputServiceServer(grpcServer, s)
}

// Methods for Capabilities service.
func (s *Server) GetCapabilities(ctx context.Context, in *re.GetCapabilitiesRequest) (*re.ServerCapabilities, error) {
	digestFunction := re.DigestFunction_GITSHA1
	return &re.ServerCapabilities{
		CacheCapabilities: &re.CacheCapabilities{
			DigestFunctions:               []re.DigestFunction_Value{digestFunction},
			ActionCacheUpdateCapabilities: &re.ActionCacheUpdateCapabilities{UpdateEnabled: true},
			MaxBatchTotalSizeBytes:        -1,
			SymlinkAbsolutePathStrategy:   re.SymlinkAbsolutePathStrategy_DISALLOWED,
			SupportedCompressors:          []re.Compressor_Value{},
		},
		ExecutionCapabilities: &re.ExecutionCapabilities{
			DigestFunction: digestFunction,
			ExecEnabled:    true,
			ExecutionPriorityCapabilities: &re.PriorityCapabilities{
				Priorities: []*re.PriorityCapabilities_PriorityRange{
					{
						MinPriority: -100,
						MaxPriority: 100,
					},
				},
			},
		},
		DeprecatedApiVersion: &sv.SemVer{},
		LowApiVersion:        &sv.SemVer{Major: 2},
		HighApiVersion:       &sv.SemVer{Major: 2},
	}, nil
}

func (s *Server) GetActionResult(ctx context.Context, in *re.GetActionResultRequest) (*re.ActionResult, error) {
	result, err := s.getActionResult(ctx, in)
	_, statusErr := status.FromError(err)
	if err != nil && !statusErr {
		log.Printf("FAILED GetActionResult: %v %v", in.ActionDigest, err)
	}
	//log.Printf("GetActionResult result: %v %v", result, err)
	return result, err
}

func (s *Server) getActionResult(ctx context.Context, in *re.GetActionResultRequest) (*re.ActionResult, error) {
	start := time.Now()

	defer NewSpan("GetActionResult(Instance=%s, Action=%s)", in.InstanceName, in.ActionDigest.Hash).End()

	build, err := s.actionToBuild(in.ActionDigest.Hash)
	if err != nil {
		return nil, err
	}

	log.Printf("getActionResult() - actionToBuild() %s", time.Since(start))
	start = time.Now()

	evalRespHash, err := s.singleMappedValue(ctx, build.evalReq.Hash)
	if err != nil {
		return nil, err
	}

	log.Printf("getActionResult() - singleMappedValue() %s", time.Since(start))
	start = time.Now()

	if evalRespHash.IsZero() {
		return nil, status.Error(codes.NotFound, "The requested action result could not be found.")
	}
	evalResp, err := binding.FetchObject[binding.EvalResponse](ctx, s.store, evalRespHash)
	if err != nil {
		return nil, err
	}

	log.Printf("getActionResult() - fetchObject() %s", time.Since(start))
	start = time.Now()

	actionResult, err := s.evalToActionResult(evalResp, build)
	if err != nil {
		return nil, err
	}

	log.Printf("getActionResult() - evalToActionResult() %s", time.Since(start))

	return actionResult, nil
}

func (s *Server) UpdateActionResult(ctx context.Context, in *re.UpdateActionResultRequest) (*re.ActionResult, error) {
	defer NewSpan("UpdateActionResult(Instance=%s, Action=%s)", in.InstanceName, in.ActionDigest.Hash).End()

	result, err := s.updateActionResult(ctx, in)
	if err != nil {
		log.Printf("FAILED UpdateActionResult: %v %v", in.ActionDigest, err)
	}
	return result, err
}

func (s *Server) updateActionResult(ctx context.Context, in *re.UpdateActionResultRequest) (*re.ActionResult, error) {
	build, err := s.actionToBuild(in.ActionDigest.Hash)
	if err != nil {
		return nil, err
	}
	evalResp, err := s.actionResultToEvalResp(build, in.ActionResult)
	if err != nil {
		return nil, err
	}

	if err := s.addCacheEntry(ctx, build.evalReq, evalResp); err != nil {
		return nil, err
	}
	log.Printf("wrote new cache-entry update for eval-req=%s => eval-resp=%s", build.evalReq.Hash, evalResp.Hash)

	return in.ActionResult, nil
}

func (s *Server) fetchObject(hash entities.Hash) ([]byte, error) {
	return store.ReadAllBlob(context.Background(), s.store, hash)
}

func fetchObjectHexHash[T any](hash string, s *Server) (*T, error) {
	var obj T
	if !entities.IsHash(hash) {
		return nil, fmt.Errorf("expected to see an ABFS hash, got %s", hash)
	}
	buf, err := s.fetchObject(entities.NewHash(hash))
	if entities.IsNotExist(err) {
		return nil, status.Errorf(codes.NotFound, "The requested %T with hash=%s could not be found.", obj, hash)
	}
	objMsg := any(&obj).(proto.Message)
	if err := proto.Unmarshal(buf, objMsg); err != nil {
		log.Printf("Error unmarshalling %T: %s", obj, err)
		return nil, err
	}
	return &obj, err
}

type BuildExec struct {
	actionDigest     string
	action           *re.Action
	command          *re.Command
	inputPatch       entities.Patch
	outputsInlineMap map[string]bool
	evalReq          *binding.EvalRequest
}

func (s *Server) fixInputPatch(patch entities.Patch) entities.Patch {
	i := 0
	for _, p := range patch {
		if p.Path == "bazel-out/volatile-status.txt" {
			continue
		}
		patch[i] = p
		i++
	}
	return patch[:i]
}

func (s *Server) actionToBuild(actionDigest string) (*BuildExec, error) {
	action, err := fetchObjectHexHash[re.Action](actionDigest, s)
	if err != nil {
		return nil, err
	}

	command, err := fetchObjectHexHash[re.Command](action.CommandDigest.Hash, s)
	if err != nil {
		return nil, err
	}

	var inputPatch entities.Patch
	if err := s.addDirectoyToPatch("", action.InputRootDigest.Hash, &inputPatch); err != nil {
		return nil, err
	}

	// OutputFiles is still used in older versions of bazel
	//nolint:staticcheck
	outputsInlineMap := make(map[string]bool, len(command.OutputFiles)+len(command.OutputDirectories)+len(command.OutputPaths))
	//nolint:staticcheck
	for _, outPaths := range [][]string{command.OutputFiles, command.OutputDirectories, command.OutputPaths} {
		for _, p := range outPaths {
			outputsInlineMap[p] = false
		}
	}

	targets := make([]string, 0, len(outputsInlineMap))
	for p := range outputsInlineMap {
		targets = append(targets, p)
	}

	evalReq := &binding.EvalRequest{
		Version:    binding.CurrentEvalRequestVersion + "-bazel",
		Targets:    targets,
		DepFile:    "",
		Command:    strings.Join(command.Arguments, " "), //FIXME args might need escaping
		InputPatch: s.fixInputPatch(inputPatch),
		RSP:        "",
		EnvVars:    binding.Hash{}, //FIXME implement
	}
	if err := evalReq.Encode(); err != nil {
		return nil, err
	}
	return &BuildExec{
		actionDigest:     actionDigest,
		action:           action,
		command:          command,
		inputPatch:       inputPatch,
		evalReq:          evalReq,
		outputsInlineMap: make(map[string]bool),
	}, nil
}

// Methods for Execution service.
func (s *Server) Execute(in *re.ExecuteRequest, out re.Execution_ExecuteServer) error {
	defer NewSpan("Execute(Instance=%s, Action=%s)", in.InstanceName, in.ActionDigest.Hash).End()

	ctx := context.Background()

	build, err := s.actionToBuild(in.ActionDigest.Hash)
	if err != nil {
		return err
	}

	for _, p := range in.InlineOutputFiles {
		build.outputsInlineMap[p] = true
	}

	opResult, err := s.handleExecution(ctx, build)
	if err != nil {
		log.Printf("failed to run action %s (eval-req=%s): %v", in.ActionDigest.Hash, build.evalReq.Hash, err)
		return err
	}

	return out.Send(opResult)
}

func (s *Server) singleMappedValue(ctx context.Context, req entities.Hash) (entities.Hash, error) {
	if s.forceRun {
		return entities.Hash{}, nil
	}

	evalResps, err := s.store.MapResults(ctx, req)
	if err != nil {
		return entities.Hash{}, err
	}
	if len(evalResps) > 0 {
		return evalResps[len(evalResps)-1], nil
	}

	if err := s.client.Store().RefreshMapResults(ctx, req); err != nil {
		return entities.Hash{}, err
	}

	evalResps, err = s.store.MapResults(ctx, req)
	if err != nil {
		return entities.Hash{}, err
	}
	if len(evalResps) > 0 {
		return evalResps[len(evalResps)-1], nil
	}

	return entities.Hash{}, nil
}

func (s *Server) execution(root string, build *BuildExec) (*binding.EvalResponse, error) {
	inputPatch := build.inputPatch
	for _, p := range build.evalReq.Targets {
		inputPatch = append(inputPatch, &entities.PatchEntry{
			Path: path.Dir(p),
			Mode: entities.FileModeDir,
			Hash: entities.EmptyDirHash,
			Size: 0,
		})
	}

	var newRoot string
	if s.nsjail {
		newRoot = strings.TrimPrefix(root, "/src/")
		newRoot = "chroot/" + newRoot
	} else {
		newRoot = root
	}

	if err := s.client.ApplyPatch(nil, inputPatch, nil, newRoot); err != nil {
		log.Printf("pepe: root: %s %s", root, err)
		buf, _ := json.MarshalIndent(inputPatch, "", "  ")
		log.Printf("pepe: buf: %s", buf)
		return nil, err
	}

	start := time.Now()
	cmd := exec.Command(build.command.Arguments[0], build.command.Arguments[1:]...)
	cmd.Dir = root
	if build.command.WorkingDirectory != "" {
		cmd.Dir = path.Join(cmd.Dir, build.command.WorkingDirectory)
	}
	if err := os.MkdirAll(cmd.Dir, 0755); err != nil {
		return nil, err
	}
	cmd.Env = make([]string, len(build.command.EnvironmentVariables))
	for i, kv := range build.command.EnvironmentVariables {
		cmd.Env[i] = kv.Name + "=" + kv.Value
	}
	var stdout, stdin bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdin
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		//log.Printf("pepe %v", build.command.Arguments)
		//log.Printf("pepe2 %v", cmd)
		// log.Printf("pepe3 cmd.Dir: %s", cmd.Dir)
		// log.Printf("pepe3 build.command.Arguments: %v", build.command.Arguments)
		return nil, err
	}

	writePatch := make(entities.Patch, 0, len(build.evalReq.Targets))
	for _, p := range build.evalReq.Targets {
		if err := addLocalFileToPatch(cmd.Dir, p, &writePatch); err != nil {
			if entities.IsNotExist(err) {
				// return nil, fmt.Errorf("cannot find output file %s: %w", p, err)
				continue
			}
			return nil, fmt.Errorf("failed to add local dir (%q) to patch: %w", p, err)
		}
	}

	resp := &binding.EvalResponse{
		Version:     binding.CurrentEvalResponseVersion,
		EvalRequest: build.evalReq.Hash,
		Stdout:      stdout.String(),
		Stderr:      stdin.String(),
		ExitCode:    cmd.ProcessState.ExitCode(),
		// FIXME add RunInfo
		// FIXME add ReadPatch
		WritePatch: writePatch,
		// FIXME add Deletes
	}
	resp.RunInfo.RunDuration = time.Since(start).Seconds()
	if err := resp.Encode(); err != nil {
		return nil, err
	}

	return resp, nil
}

func addLocalFileToPatch(root, p string, patch *entities.Patch) error {
	fullPath := path.Join(root, p)
	stat, err := os.Lstat(fullPath)
	if err != nil {
		return err
	}
	hash, err := bindcas.Hash(fullPath)
	if err != nil {
		return fmt.Errorf("failed to read file hash %q: %w", p, err)
	}
	var mode entities.FileMode
	switch {
	case stat.IsDir():
		ents, err := os.ReadDir(fullPath)
		if err != nil {
			return fmt.Errorf("failed to open dir %q: %w", p, err)
		}
		for _, ent := range ents {
			if err := addLocalFileToPatch(root, path.Join(p, ent.Name()), patch); err != nil {
				return err
			}
		}
		mode = entities.FileModeDir
	case stat.Mode()&os.ModeSymlink != 0:
		mode = entities.FileModeSymlink
	case stat.Mode()&0111 != 0:
		mode = entities.FileModeExecutable
	default:
		mode = entities.FileModeRegular
	}
	*patch = append(*patch, &entities.PatchEntry{
		Path: p,
		Mode: mode,
		Hash: hash,
		Size: stat.Size(),
	})

	return nil
}

func (s *Server) actionResultToEvalResp(build *BuildExec, actionResult *re.ActionResult) (*binding.EvalResponse, error) {
	writePatch := make(entities.Patch, 0, len(actionResult.OutputFiles)+len(actionResult.OutputDirectories)+len(actionResult.OutputSymlinks))
	for _, f := range actionResult.OutputFiles {
		mode := entities.FileModeRegular
		if f.IsExecutable {
			mode = entities.FileModeExecutable
		}
		writePatch = append(writePatch, &entities.PatchEntry{
			Path: f.Path,
			Size: f.Digest.SizeBytes,
			Hash: entities.NewHash(f.Digest.Hash),
			Mode: mode,
		})
	}
	for _, d := range actionResult.OutputDirectories {
		writePatch = append(writePatch, &entities.PatchEntry{
			Path: d.Path,
			Size: 0,
			Hash: entities.EmptyDirHash,
			Mode: entities.FileModeDir,
		})
	}
	for _, sym := range actionResult.OutputSymlinks {
		hash, size, err := store.StoreBlob(context.Background(), s.store, []byte(sym.Target))
		if err != nil {
			return nil, err
		}
		writePatch = append(writePatch, &entities.PatchEntry{
			Path: sym.Path,
			Size: size,
			Hash: hash,
			Mode: entities.FileModeDir,
		})
	}

	evalResp := &binding.EvalResponse{
		Version:     binding.CurrentEvalResponseVersion,
		EvalRequest: build.evalReq.Hash,
		ExitCode:    int(actionResult.ExitCode),
		Stdout:      string(actionResult.StdoutRaw),
		Stderr:      string(actionResult.StderrRaw),
		WritePatch:  writePatch,
	}
	if err := evalResp.Encode(); err != nil {
		return nil, err
	}
	return evalResp, nil
}

func (s *Server) evalToActionResult(evalResp *binding.EvalResponse, build *BuildExec) (result *re.ActionResult, retErr error) {
	outputsInlineMap := build.outputsInlineMap
	outputFiles := make([]*re.OutputFile, 0, len(evalResp.WritePatch))
	outputSyms := make([]*re.OutputSymlink, 0)
	for _, p := range evalResp.WritePatch {
		switch p.Mode {
		case entities.FileModeRegular, entities.FileModeExecutable:
			out := &re.OutputFile{
				Path: p.Path,
				Digest: &re.Digest{
					Hash:      p.Hash.String(),
					SizeBytes: p.Size,
				},
				IsExecutable: p.Mode == entities.FileModeExecutable,
			}

			if outputsInlineMap[p.Path] {
				content, err := s.fetchObject(p.Hash)
				if err != nil {
					return nil, fmt.Errorf("failed to read inline output file %s (hash=%s): %w", p.Path, p.Hash, err)
				}
				out.Contents = content
			}
			outputFiles = append(outputFiles, out)
		case entities.FileModeSymlink:
			target, err := s.fetchObject(p.Hash)
			if err != nil {
				return nil, err
			}

			out := &re.OutputSymlink{
				Path:   p.Path,
				Target: string(target),
			}

			outputSyms = append(outputSyms, out)
		case entities.FileModeDir:
			// Do nothing for now
		default:
			return nil, fmt.Errorf("file %s is not a regular file", p.Path)
		}
	}

	var outputDirs []string
	//nolint: staticcheck // OutputDirectories is still used in bazel
	outputDirs = append(outputDirs, build.command.OutputDirectories...)
	for _, p := range build.command.OutputPaths {
		// check if it is a directory in WritePatch
		for _, entry := range evalResp.WritePatch {
			if entry.Path == p && entry.Mode == entities.FileModeDir {
				outputDirs = append(outputDirs, p)
				break
			}
		}
	}

	var outputDirectories []*re.OutputDirectory
	for _, outDir := range outputDirs {
		tree, err := s.buildTree(outDir, evalResp.WritePatch)
		if err != nil {
			return nil, fmt.Errorf("failed to build tree for %s: %w", outDir, err)
		}
		treeData, err := proto.Marshal(tree)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal tree for %s: %w", outDir, err)
		}
		h, _, err := store.StoreBlob(context.Background(), s.store, treeData)
		if err != nil {
			return nil, fmt.Errorf("failed to store tree for %s: %w", outDir, err)
		}
		outputDirectories = append(outputDirectories, &re.OutputDirectory{
			Path: outDir,
			TreeDigest: &re.Digest{
				Hash:      h.String(),
				SizeBytes: int64(len(treeData)),
			},
		})
	}

	stdoutHash, stdoutInline, err := s.inlineOrStore([]byte(evalResp.Stdout))
	if err != nil {
		return nil, err
	}
	stderrHash, stderrInline, err := s.inlineOrStore([]byte(evalResp.Stderr))
	if err != nil {
		return nil, err
	}
	result = &re.ActionResult{
		ExitCode:                int32(evalResp.ExitCode),
		StdoutRaw:               []byte(stdoutInline),
		StderrRaw:               []byte(stderrInline),
		OutputFiles:             outputFiles,
		OutputFileSymlinks:      []*re.OutputSymlink{},
		OutputSymlinks:          outputSyms,
		OutputDirectories:       outputDirectories,
		OutputDirectorySymlinks: []*re.OutputSymlink{},
	}
	if !stdoutHash.IsZero() {
		result.StdoutDigest = &re.Digest{
			Hash:      stdoutHash.String(),
			SizeBytes: int64(len(stdoutInline)),
		}
	}
	if !stderrHash.IsZero() {
		result.StderrDigest = &re.Digest{
			Hash:      stderrHash.String(),
			SizeBytes: int64(len(stderrInline)),
		}
	}
	return result, nil
}

const inlineThreashold = 1024 * 1024 // 1 MB

func (s *Server) inlineOrStore(b []byte) (entities.Hash, string, error) {
	if len(b) < inlineThreashold {
		return entities.Hash{}, string(b), nil
	}
	hash, _, err := store.StoreBlob(context.Background(), s.store, b)
	if err != nil {
		return entities.Hash{}, "", err
	}
	return hash, "", nil
}

func (s *Server) handleExecution(ctx context.Context, build *BuildExec) (*longrunningpb.Operation, error) {

	evalRespHash, err := s.singleMappedValue(ctx, build.evalReq.Hash)
	if err != nil {
		return nil, err
	}

	if !evalRespHash.IsZero() {
		evalResp, err := binding.FetchObject[binding.EvalResponse](ctx, s.store, evalRespHash)
		if err != nil {
			return nil, err
		}
		actionResult, err := s.evalToActionResult(evalResp, build)
		if err != nil {
			return nil, err
		}

		pbExecResp, err := anypb.New(&re.ExecuteResponse{
			Result:       actionResult,
			CachedResult: false,
			// Status:       &status.Status{},
			ServerLogs: map[string]*re.LogFile{},
			Message:    "",
		})

		if err != nil {
			return nil, fmt.Errorf("failed to marshal ActionResult to Any: %w", err)
		}
		log.Printf("cache-hit for eval-req=%s => eval-resp=%s", build.evalReq.Hash, evalResp.Hash)
		return &longrunningpb.Operation{
			Done: true,
			Result: &longrunningpb.Operation_Response{
				Response: pbExecResp,
			},
		}, nil
	}

	for p := range build.outputsInlineMap {
		build.inputPatch = append(build.inputPatch, &entities.PatchEntry{
			Path: path.Dir(p),
			Mode: entities.FileModeDir,
			Hash: entities.EmptyDirHash,
			Size: 0,
		})
	}

	execRoot := path.Join(s.root, s.buildOut, uuid.New().String())
	// log.Printf("pepe4: execRoot: %s", execRoot)
	if err := os.MkdirAll(execRoot, 0755); err != nil {
		return nil, err
	}
	// defer os.RemoveAll(execRoot)

	evalResp, err := s.execution(execRoot, build)
	if err != nil {
		// Temporarily persist for debugging.
		if err := s.store.PutObject(ctx, build.evalReq.Hash, build.evalReq.Encoded); err != nil {
			log.Printf("failed to persist eval-req=%s: %v", build.evalReq.Hash, err)
		}
		return nil, err
	}

	if err := s.addCacheEntry(ctx, build.evalReq, evalResp); err != nil {
		return nil, err
	}

	actionResult, err := s.evalToActionResult(evalResp, build)
	if err != nil {
		return nil, err
	}
	pbExecResp, err := anypb.New(&re.ExecuteResponse{
		Result:       actionResult,
		CachedResult: false,
		// Status:       &status.Status{},
		ServerLogs: map[string]*re.LogFile{},
		Message:    "",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ActionResult to Any: %w", err)
	}
	return &longrunningpb.Operation{
		Done: true,
		Result: &longrunningpb.Operation_Response{
			Response: pbExecResp,
		},
	}, nil
}

func (s *Server) addCacheEntry(ctx context.Context, evalReq *binding.EvalRequest, evalResp *binding.EvalResponse) error {
	targets := strings.Join(evalReq.Targets, ",")
	if err := s.store.PutObject(ctx, evalReq.Hash, evalReq.Encoded); err != nil {
		return err
	}
	if err := s.store.PutObject(ctx, evalResp.Hash, evalResp.Encoded); err != nil {
		return err
	}
	if evalResp.ExitCode != 0 {
		log.Printf("error executing %s with exit code %d eval-req=%s => eval-resp=%s",
			targets, evalResp.ExitCode, evalReq.Hash, evalResp.Hash)
		return nil
	}
	if err := s.store.AddMapResult(ctx, evalReq.Hash, evalResp.Hash); err != nil {
		return err
	}
	log.Printf("wrote new cache-entry for %s executed eval-req=%s => eval-resp=%s", targets, evalReq.Hash, evalResp.Hash)
	return nil
}

func (s *Server) fetchDirectory(dirHash string) (*re.Directory, error) {
	if dir, ok := s.directories.Get(dirHash); ok {
		return dir, nil
	}
	dir, err := fetchObjectHexHash[re.Directory](dirHash, s)
	if err != nil {
		return nil, err
	}
	s.directories.Add(dirHash, dir)
	return dir, nil
}

func (s *Server) addDirectoyToPatch(root string, dirHash string, patch *entities.Patch) error {
	pathPrefix := root
	if root != "" {
		pathPrefix += string(os.PathSeparator)
	}

	init := len(*patch)

	directory, err := s.fetchDirectory(dirHash)
	if err != nil {
		return err
	}

	for _, file := range directory.Files {
		mode := entities.FileModeRegular
		if file.IsExecutable {
			mode = entities.FileModeExecutable
		}
		if !entities.IsHash(file.Digest.Hash) {
			return fmt.Errorf("expected to see an ABFS hash for %s, got %q", root+"/"+file.Name, file.Digest.Hash)
		}
		hash := entities.NewHash(file.Digest.Hash)
		*patch = append(*patch, &entities.PatchEntry{
			Path: pathPrefix + file.Name,
			Mode: mode,
			Hash: hash,
			Size: file.Digest.SizeBytes,
		})
	}

	for _, sym := range directory.Symlinks {
		hash, size, err := store.StoreBlob(context.Background(), s.store, []byte(sym.Target))
		if err != nil {
			return err
		}
		*patch = append(*patch, &entities.PatchEntry{
			Path: pathPrefix + sym.Name,
			Mode: entities.FileModeSymlink,
			Hash: hash,
			Size: size,
		})
	}

	for _, dir := range directory.Directories {
		if err := s.addDirectoyToPatch(pathPrefix+dir.Name, dir.Digest.Hash, patch); err != nil {
			return err
		}
	}

	if len(*patch) == init {
		*patch = append(*patch, &entities.PatchEntry{
			Path: root,
			Mode: entities.FileModeDir,
			Hash: entities.EmptyDirHash,
			Size: 0,
		})
	}
	return nil
}

func (s *Server) WaitExecution(in *re.WaitExecutionRequest, out re.Execution_WaitExecutionServer) error {
	defer NewSpan("WaitExecution(Name=%s)", in.Name).End()

	return nil
}

// FindMissingBlobs parallelizes the checking of blob existence to improve performance.
// It uses a worker pool pattern to limit the number of concurrent operations
// and ensures thread-safe collection of missing digests and error handling.
func (s *Server) FindMissingBlobs(ctx context.Context, req *re.FindMissingBlobsRequest) (*re.FindMissingBlobsResponse, error) {
	defer NewSpan("FindMissingBlobs(Digests=%d)", len(req.BlobDigests)).End()

	misses := make(chan *re.Digest, len(req.BlobDigests))

	grp, ctx := errgroup.WithContext(ctx)
	grp.SetLimit(runtime.NumCPU() * 2)

	for _, d := range req.BlobDigests {
		grp.Go(func() error {
			if !entities.IsHash(d.Hash) {
				return fmt.Errorf("expected to see an ABFS hash, got %q", d.Hash)
			}
			reader, err := s.store.OpenBlob(ctx, entities.NewHash(d.Hash))
			if entities.IsNotExist(err) {
				misses <- d
				return nil
			}
			if err != nil {
				return err
			}
			return reader.Close()
		})
	}

	if err := grp.Wait(); err != nil {
		return nil, err
	}
	close(misses)

	resp := &re.FindMissingBlobsResponse{
		MissingBlobDigests: make([]*re.Digest, 0, len(misses)),
	}
	for d := range misses {
		resp.MissingBlobDigests = append(resp.MissingBlobDigests, d)
	}
	log.Printf("FindMissingBlobs completed. Found %d missing blobs.", len(resp.MissingBlobDigests))
	return resp, nil
}

func (s *Server) BatchUpdateBlobs(context.Context, *re.BatchUpdateBlobsRequest) (*re.BatchUpdateBlobsResponse, error) {
	defer NewSpan("BatchUpdateBlobs()").End()

	return nil, status.Errorf(codes.Unimplemented, "method BatchUpdateBlobs not implemented")
}

func (s *Server) BatchReadBlobs(context.Context, *re.BatchReadBlobsRequest) (*re.BatchReadBlobsResponse, error) {
	defer NewSpan("BatchReadBlobs()").End()

	return nil, status.Errorf(codes.Unimplemented, "method BatchReadBlobs not implemented")
}

func (s *Server) GetTree(*re.GetTreeRequest, re.ContentAddressableStorage_GetTreeServer) error {
	defer NewSpan("GetTree()").End()

	return status.Errorf(codes.Unimplemented, "method GetTree not implemented")
}

func (s *Server) SplitBlob(context.Context, *re.SplitBlobRequest) (*re.SplitBlobResponse, error) {
	defer NewSpan("SplitBlob()").End()

	return nil, status.Errorf(codes.Unimplemented, "method SplitBlob not implemented")
}

func (s *Server) SpliceBlob(context.Context, *re.SpliceBlobRequest) (*re.SpliceBlobResponse, error) {
	defer NewSpan("SpliceBlob()").End()

	return nil, status.Errorf(codes.Unimplemented, "method SpliceBlob not implemented")
}

// Methods for ByteStream service.
func (s *Server) Read(req *bytestream.ReadRequest, resp bytestream.ByteStream_ReadServer) (retErr error) {
	defer NewSpan("Read(Resource=%s, Offset=%d, Limit=%d)", req.ResourceName, req.ReadOffset, req.ReadLimit).End()

	hash, size, err := HashFromResourceName(req.ResourceName)
	if err != nil {
		return err
	}
	reader, err := s.store.OpenBlob(context.Background(), hash)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(reader, &retErr)
	_, err = io.CopyN(io.Discard, reader, req.ReadOffset)
	if err != nil {
		return err
	}
	left := req.ReadLimit
	if left == 0 {
		left = size - req.ReadOffset
	}
	buf := make([]byte, 32*1024)
	for left > 0 {
		readResp := new(bytestream.ReadResponse)
		n, err := reader.Read(buf)
		if err != nil && err != io.EOF {
			return err
		}
		readResp.Data = buf[:n]
		if err := resp.Send(readResp); err != nil {
			return err
		}
		left -= int64(n)
	}
	return nil
}

func HashFromResourceName(resource string) (entities.Hash, int64, error) {
	// For sha1, resource names are of the form:
	// uploads/3deab338-03ba-469e-aa4b-1f363fb90a62/blobs/b58bbc6eac9b72800a542eddddfbe7eef68281b1ed74835e5fcc34264e563300/123

	// For blake3, resource names are of the form:
	// uploads/e8804ab2-11f6-41e6-bbb7-5366780af530/blobs/blake3/10ff80280f849d8a89000d87d2268d16a7f28de6/1317
	// For gitsha1, resource names are of the form:
	// uploads/e8804ab2-11f6-41e6-bbb7-5366780af530/blobs/gitsha1/10ff80280f849d8a89000d87d2268d16a7f28de6/1317
	log.Println("Resource name: ", resource)
	marker := "blobs/gitsha1/"
	pos := strings.Index(resource, marker)
	if pos < 0 {
		return entities.Hash{}, 0, fmt.Errorf("did not find %s in resource name: %q", marker, resource)
	}
	d := strings.Split(resource[pos+len(marker):], "/")
	if len(d) != 2 {
		return entities.Hash{}, 0, fmt.Errorf("expected resource name of form .../%s/<hash>/<size>, got %q", marker, resource)
	}
	if !entities.IsHash(d[0]) {
		return entities.Hash{}, 0, fmt.Errorf("expected to see an ABFS hash for %s, got %s", resource, d[0])
	}
	key := entities.NewHash(d[0])
	size, err := strconv.ParseInt(d[1], 10, 64)
	if err != nil {
		return entities.Hash{}, 0, fmt.Errorf("expected size to be an int64 for %s, got %s: %w", resource, d[11], err)
	}
	return key, size, nil
}

func (s *Server) Write(srv bytestream.ByteStream_WriteServer) error {
	defer NewSpan("Write()").End()

	ctx := context.Background()

	reader, writer := io.Pipe()

	var key entities.Hash
	var group errgroup.Group
	var size, resSize int64
	for i := 0; ; i++ {
		req, err := srv.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			writer.CloseWithError(err)
			return err
		}
		if i == 0 {
			key, resSize, err = HashFromResourceName(req.ResourceName)
			if err != nil {
				return err
			}
			group.Go(func() error {
				defer func() {
					_, _ = io.Copy(io.Discard, reader)
				}()
				return s.store.WriteBlob(ctx, key, resSize, reader)
			})
		}

		s.tryDirectoryCapture(key, req.Data)

		n, err := writer.Write(req.Data)
		if err != nil {
			writer.CloseWithError(err)
			return fmt.Errorf("error while writing the object %s: %w", key, err)
		}
		size += int64(n)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("error while closing writer for %s: %w", key, err)
	}
	if err := group.Wait(); err != nil {
		return fmt.Errorf("error while writing %s: %w", key, err)
	}

	log.Printf("Write %s size=%d", key, size)

	var resp bytestream.WriteResponse
	resp.CommittedSize = size
	return srv.SendAndClose(&resp)
}

func (s *Server) tryDirectoryCapture(key entities.Hash, data []byte) {
	dir := new(re.Directory)
	if err := proto.Unmarshal(data, dir); err != nil {
		return // It is best effort
	}
	s.directories.Add(key.String(), dir)
}

func (s *Server) QueryWriteStatus(ctx context.Context, req *bytestream.QueryWriteStatusRequest) (*bytestream.QueryWriteStatusResponse, error) {
	defer NewSpan("QueryWriteStatus()").End()

	return nil, nil
}

type treeNode struct {
	name      string
	files     []*re.FileNode
	symlinks  []*re.SymlinkNode
	subdirs   map[string]*treeNode
	dirProto  *re.Directory
	dirDigest *re.Digest
}

func newTreeNode(name string) *treeNode {
	return &treeNode{
		name:    name,
		subdirs: make(map[string]*treeNode),
	}
}

func sortFileNodes(nodes []*re.FileNode) {
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})
}

func sortDirectoryNodes(nodes []*re.DirectoryNode) {
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})
}

func sortSymlinkNodes(nodes []*re.SymlinkNode) {
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})
}

func (s *Server) buildTree(outDir string, writePatch entities.Patch) (*re.Tree, error) {
	rootNode := newTreeNode("")

	prefix := outDir + "/"
	found := false
	for _, entry := range writePatch {
		if entry.Path == outDir && entry.Mode == entities.FileModeDir {
			found = true
			continue
		}
		if !strings.HasPrefix(entry.Path, prefix) {
			continue
		}
		found = true
		relPath := strings.TrimPrefix(entry.Path, prefix)
		parts := strings.Split(relPath, "/")

		curr := rootNode
		for i := 0; i < len(parts)-1; i++ {
			part := parts[i]
			if _, ok := curr.subdirs[part]; !ok {
				curr.subdirs[part] = newTreeNode(part)
			}
			curr = curr.subdirs[part]
		}

		fileName := parts[len(parts)-1]
		switch entry.Mode {
		case entities.FileModeDir:
			if _, ok := curr.subdirs[fileName]; !ok {
				curr.subdirs[fileName] = newTreeNode(fileName)
			}
		case entities.FileModeSymlink:
			target, err := s.fetchObject(entry.Hash)
			if err != nil {
				return nil, fmt.Errorf("failed to read symlink target %s: %w", entry.Path, err)
			}
			curr.symlinks = append(curr.symlinks, &re.SymlinkNode{
				Name:   fileName,
				Target: string(target),
			})
		default:
			isExec := entry.Mode == entities.FileModeExecutable
			fileNode := &re.FileNode{
				Name: fileName,
				Digest: &re.Digest{
					Hash:      entry.Hash.String(),
					SizeBytes: entry.Size,
				},
				IsExecutable: isExec,
			}
			curr.files = append(curr.files, fileNode)
		}
	}

	if !found {
		return &re.Tree{
			Root: &re.Directory{},
		}, nil
	}

	var children []*re.Directory

	var walk func(node *treeNode) error
	walk = func(node *treeNode) error {
		node.dirProto = &re.Directory{
			Files:    node.files,
			Symlinks: node.symlinks,
		}

		for name, sub := range node.subdirs {
			if err := walk(sub); err != nil {
				return err
			}

			node.dirProto.Directories = append(node.dirProto.Directories, &re.DirectoryNode{
				Name:   name,
				Digest: sub.dirDigest,
			})
			children = append(children, sub.dirProto)
		}

		sortFileNodes(node.dirProto.Files)
		sortDirectoryNodes(node.dirProto.Directories)
		sortSymlinkNodes(node.dirProto.Symlinks)

		data, err := proto.Marshal(node.dirProto)
		if err != nil {
			return err
		}
		h, _, err := store.StoreBlob(context.Background(), s.store, data)
		if err != nil {
			return err
		}
		node.dirDigest = &re.Digest{
			Hash:      h.String(),
			SizeBytes: int64(len(data)),
		}
		return nil
	}

	if err := walk(rootNode); err != nil {
		return nil, err
	}

	return &re.Tree{
		Root:     rootNode.dirProto,
		Children: children,
	}, nil
}
