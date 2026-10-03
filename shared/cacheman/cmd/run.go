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

package cacheman

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/shared/cacheman/server"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
)

type runCmd struct {
	*cachemanCmd
	debug   bool
	port    int
	printer server.Printer
}

func NewRunCmd(c *cachemanCmd) *runCmd {
	return &runCmd{cachemanCmd: c}
}

func (c *runCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:   "run",
		Usage:  "",
		Desc:   `Run the cacheman daemon and optionally with the necessary SSH tunnel.`,
		Daemon: true,
	}
}

func (c *runCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.debug, "debug", "d", false, "Show debug log messages")
	fl.IntVarP(&c.port, "port", "p", 0, "HTTP port to listen")
	fl.StringP("logfile", "l", "", "Deprecated. Only serves to not completely break backwards compatibility")
}

func (c *runCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	c.printer = &server.DefaultPrinter{}

	return c.run(ctx)
}

func (c *runCmd) run(ctx context.Context) error {
	server.SetDebug(c.debug)

	s, err := server.New(c.Instance, c.newARES)
	if err != nil {
		return err
	}
	c.printer = s.Printer()

	go func() {
		lis, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", c.port))
		if err != nil {
			c.printer.Fatalf("Could not bind to port %d: %v", c.port, err)
		}
		port := lis.Addr().(*net.TCPAddr).Port
		c.printer.Printf("Starting a web-server on http://localhost:%d/\n", port)
		if err := http.Serve(lis, nil); err != nil {
			c.printer.Fatalf("Error when starting or running http server: %v", err)
		}
	}()

	var grp errgroup.Group
	grp.Go(func() error {
		<-ctx.Done()
		if err := s.Stop(); err != nil {
			c.printer.Printf("Error while stopping cacheman: %s\n", err)
		}
		return nil
	})

	grp.Go(func() error {
		return s.Serve(ctx)
	})

	return grp.Wait()
}
