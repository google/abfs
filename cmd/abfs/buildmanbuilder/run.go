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

package buildmanbuilder

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/pflag"

	"github.com/google/abfs/buildman/builder"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
)

type runCmd struct {
	*buildmanBuilderCmd
	*cmd.ConfigProvider
	*cmd.StoreProvider
	debug          bool
	port           int
	jobs           int
	subscription   string
	maxMessageTime time.Duration
	skipRefPrefix  bool
}

func NewRunCmd(c *buildmanBuilderCmd) *runCmd {
	return &runCmd{buildmanBuilderCmd: c}
}

func (c *runCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "run",
		Usage:   "",
		Aliases: []string{},
		Desc:    `Run the buildmanbuilder daemon`,
	}
}

func (c *runCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.debug, "debug", "d", false, "Show debug log messages")
	fl.IntVarP(&c.port, "port", "p", 0, "HTTP port to listen")
	fl.IntVarP(&c.jobs, "jobs", "j", 1, "Maximum number of concurrent builds")
	fl.StringVar(&c.subscription, "subscription", "build-request-local-sub", "Subscription from which messages should be read")
	fl.DurationVar(&c.maxMessageTime, "max-message-time", 3*time.Hour, "Maximum time a worker has to process a message")
	fl.BoolVar(&c.skipRefPrefix, "skip-ref-prefix", false, "Ignore the ref prefix provided by the build requests. Useful in a staging environment where buildman is testing changes.")
}

func (c *runCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)
	config := c.Config
	if c.logPath == "" {
		c.logPath = filepath.Join(config.LogDir, "buildmanbuilder")
	}
	metrics, err := builder.MetricsForRegistration(config.ManifestServer)
	if err != nil {
		return err
	}
	prometheus.MustRegister(metrics...)

	builder.SetDebug(c.debug)

	if c.jobs < 1 {
		return clierr.CLIErr("--jobs less than 1 will do no work")
	}

	tracker := builder.NewStatusTracker(c.jobs, c.subscription)

	go func() {
		// listenting on all network interfaces (not just localhost) because
		// prometheus needs to scrape our metrics
		lis, err := net.Listen("tcp", fmt.Sprintf(":%d", c.port))
		if err != nil {
			log.Fatalf("ERROR: Could not bind to port %d: %v", c.port, err)
		}
		http.Handle("/metrics", promhttp.Handler())
		http.Handle("/info", tracker)
		port := lis.Addr().(*net.TCPAddr).Port
		log.Printf("Starting a web-server on http://localhost:%d/ (but it's available off of localhost)", port)
		if err := http.Serve(lis, nil); err != nil {
			log.Fatalf("ERROR: when starting or running http server: %v", err)
		}
	}()

	return c.run(ctx, tracker)
}

func (c *runCmd) run(ctx context.Context, tracker *builder.StatusTracker) error {
	// we can add other receivers here in the future
	p, err := newGcpPubSubRequestReceiver(ctx, c.Config.RemoteProject, c.subscription, c.jobs, c.maxMessageTime)
	if err != nil {
		return err
	}

	abfsBin, err := os.Executable()
	if err != nil {
		return err
	}
	b := &builder.BuildManBuilder{
		ABFSBin:       abfsBin,
		Receiver:      p,
		Store:         c.Store,
		SkipRefPrefix: c.skipRefPrefix,
		Tracker:       tracker,
	}

	return b.Serve(ctx)
}
