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

package buildmanscheduler

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"path/filepath"

	"github.com/spf13/pflag"

	"github.com/google/abfs/buildman/scheduler"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
)

type runCmd struct {
	*buildmanSchedulerCmd
	*cmd.ConfigProvider
	debug bool
	port  int
}

func NewRunCmd(c *buildmanSchedulerCmd) *runCmd {
	return &runCmd{buildmanSchedulerCmd: c}
}

func (c *runCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "run",
		Usage:   "",
		Aliases: []string{},
		Desc:    `Run the buildmanscheduler daemon`,
	}
}

func (c *runCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.debug, "debug", "d", false, "Show debug log messages")
	fl.IntVarP(&c.port, "port", "p", 0, "HTTP port to listen")
}

func (c *runCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	config := c.Config
	if c.logPath == "" {
		c.logPath = filepath.Join(config.LogDir, "buildmanscheduler")
	}

	scheduler.SetDebug(c.debug)

	s := scheduler.New()

	go func() {
		// currently only opening a web server for pprof profiling
		lis, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", c.port))
		if err != nil {
			log.Fatalf("Could not bind to port %d: %v", c.port, err)
		}
		port := lis.Addr().(*net.TCPAddr).Port
		log.Printf("Starting a web-server on http://localhost:%d/", port)
		if err := http.Serve(lis, nil); err != nil {
			log.Fatalf("Error when starting or running http server: %v", err)
		}
	}()

	return s.Serve(ctx)
}
