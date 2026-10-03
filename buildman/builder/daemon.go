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
	"context"
	"fmt"
	"log"
	"os/exec"
	"path"

	"github.com/google/abfs/shared/entities"
)

var debug = false

func SetDebug(d bool) {
	debug = d
}

func debugLog(format string, a ...any) {
	if debug {
		log.Printf("DEBUG: %s", fmt.Sprintf(format, a...))
	}
}

type BuildManBuilder struct {
	ABFSBin  string
	Receiver Receiver
	Store    entities.CASStore
	// should only be used for testing or similar situations
	SkipHealthCheck bool
	// should be used in an environment where requests should be processed but not stored at a ref
	SkipRefPrefix bool
	// extra flags and values to the root `abfs` command
	AbfsFlags []string
	Tracker   *StatusTracker
}

type Receiver interface {
	// Receive calls f with outstanding messages and blocks until the ctx is cancelled or a non-retryable
	// error occurs. f can be called concurrently if the provider was configured for it.
	Receive(ctx context.Context, f func(context.Context, entities.Hash) error) error
}

func (b *BuildManBuilder) Serve(ctx context.Context) error {
	log.Printf("BuildManBuilder starting...")
	debugLog("BuildManBuilder debug logs enabled")
	if !b.SkipHealthCheck {
		debugLog("Doing a health check before looking for work.")
		if err := b.healthCheck(ctx); err != nil {
			return err
		}
		debugLog("Health check passed")
	}
	debugLog("Going to listen to provider Receive()")
	return b.Receiver.Receive(ctx, func(ctx context.Context, req entities.Hash) error {
		if b.Tracker != nil {
			b.Tracker.BuildStarted(req)
		}
		w := &buildWorker{
			abfsBinDir:      path.Dir(b.ABFSBin),
			abfsRootCommand: append([]string{b.ABFSBin}, b.AbfsFlags...),
			store:           b.Store,
			skipHealthCheck: b.SkipHealthCheck,
			skipRefPrefix:   b.SkipRefPrefix,
		}
		err := w.processRequest(ctx, req)
		if b.Tracker != nil {
			b.Tracker.BuildCompleted(req, err)
		}
		if err != nil {
			log.Printf("ERROR hash %q: %v", req, err)
			return err
		}
		return nil
	})
}

func (b *BuildManBuilder) healthCheck(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, b.ABFSBin, "doctor")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("'abfs doctor' health check failed. err: %w, output: %s", err, out)
	}
	return nil
}
