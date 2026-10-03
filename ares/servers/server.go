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

package servers

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/google/abfs/eval"
	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
)

const ServerVersion = "0.1"
const ClientMinVersion = "0.1"

func SetDebug(b bool) {
	if b {
		debugLogger.SetOutput(log.Default().Writer())
	} else {
		debugLogger.SetOutput(io.Discard)
	}
}

var debugLogger = log.New(io.Discard, "DEBUG: ", log.LstdFlags)

// ARESServer is the actual golang implementation of ARES Execution
type ARESServer struct {
	abfsBin   string
	store     entities.CASStore
	cacheman  *client.Client
	evaluator *eval.Evaluator
	log       *log.Logger

	uploadTimeout time.Duration
}

var _ remote.ARESService = (*ARESServer)(nil)

type ARESConfig struct {
	UploadTimeout time.Duration

	EvalConfig eval.SetupInput
}

func NewARESServer(abfsBin string, store entities.CASStore, config ARESConfig) (*ARESServer, error) {
	cachemanClient := client.NewClient(config.EvalConfig.CacheDir, config.EvalConfig.FetchTimeout)
	evaluator, err := eval.NewEvaluator(store, cachemanClient, config.EvalConfig)
	if err != nil {
		return nil, fmt.Errorf("ARESServer: error while setting up ABFS mount: %w", err)
	}

	return &ARESServer{
		abfsBin:   abfsBin,
		store:     store,
		cacheman:  cachemanClient,
		evaluator: evaluator,
		log:       log.Default(),
	}, nil
}

func (s *ARESServer) Close() error {
	if err := s.evaluator.Close(); err != nil {
		return fmt.Errorf("ARESServer WARNING: when closing evaluator: %w", err)
	}
	return nil
}

func (s *ARESServer) Execute(ctx context.Context, evalRequestHash entities.Hash, patches []entities.Hash, storeMapResult, prefetchInputs bool) (entities.Hash, error) {
	start := time.Now()
	s.log.Printf("Execution request %q (with patches %v)", evalRequestHash, patches)
	requestCounter.Inc()

	resp, job, err := s.evaluator.Eval(ctx, evalRequestHash, patches, eval.EvalOptions{
		WriteEvalResponse: true,
		StoreMapResult:    storeMapResult,
		PrefetchInputs:    prefetchInputs,
	})
	if err != nil {
		errorCounter.Inc()
		s.log.Printf("Execution of job %+v failed in %.3fs: %v", job, time.Since(start).Seconds(), err)
		return entities.ZeroHash, err
	}

	s.log.Printf("Executed %q (with patches %v) with job durations %+v, going to wait for upload of response %s (exit-code=%d)", evalRequestHash, patches, job.Durations, resp.Hash, resp.ExitCode)
	uploadWait := time.Now()
	if err := s.cacheman.WaitFor(s.uploadTimeout, 1, []entities.Hash{resp.Hash}); err != nil {
		errorCounter.Inc()
		s.log.Printf("Execution of job %+v in %.3fs but failed to upload %s in %.3fs (exit-code=%d): %v", job, uploadWait.Sub(start).Seconds(), resp.Hash, time.Since(uploadWait).Seconds(), resp.ExitCode, err)
		return entities.ZeroHash, err
	}
	uploadSecs := time.Since(uploadWait).Seconds()
	totalSecs := time.Since(start).Seconds()

	successCounter.Inc()
	// only tracking time metrics for successful results right now
	fetchDur.Observe(job.Durations.Fetch.Seconds())
	preDur.Observe(job.Durations.Prepare.Seconds())
	runDur.Observe(job.Durations.Run.Seconds())
	postDur.Observe(job.Durations.Post.Seconds())
	uploadDur.Observe(uploadSecs)
	totalDur.Observe(totalSecs)

	// These are measured by tap, so pulled from the eval response instead of the job
	tapRunDur.Observe(resp.RunInfo.RunDuration)
	tapSnapshotDur.Observe(resp.RunInfo.SnapshotDuration)

	s.log.Printf("Executed %q (with patches %v) with job durations %+v, total %.3fs and upload response %s in %.3fs (exit-code=%d)", evalRequestHash, patches, job.Durations, totalSecs, resp.Hash, uploadSecs, resp.ExitCode)
	return resp.Hash, err
}

func (s *ARESServer) ActiveJobs() []*eval.ActiveJob {
	return s.evaluator.ActiveJobs()
}
