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
	"time"

	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/shared/cacheman/server"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/instance"
)

const defaultDrainTimeout = 8 * time.Second

type runCmd struct {
	*cachemanCmd
	debug        bool
	port         int
	drainTimeout time.Duration
	printer      server.Printer
	newServer    func(*instance.Instance, server.NewARESFunc) (cachemanServer, error)
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
	fl.DurationVar(&c.drainTimeout, "drain-timeout", defaultDrainTimeout, "How long a stop waits for pending uploads, 0 to not wait. Keep it below the time until the process is killed (30s for 'cacheman stop', and leave headroom for any exit metrics flush).")
	fl.StringP("logfile", "l", "", "Deprecated. Only serves to not completely break backwards compatibility")
}

func (c *runCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)
	if c.drainTimeout < 0 {
		return clierr.CLIErr("--drain-timeout must not be negative")
	}

	c.printer = &server.DefaultPrinter{}

	return c.run(ctx)
}

func (c *runCmd) run(ctx context.Context) error {
	server.SetDebug(c.debug)

	newServer := c.newServer
	if newServer == nil {
		newServer = func(inst *instance.Instance, newARES server.NewARESFunc) (cachemanServer, error) {
			return server.New(inst, newARES)
		}
	}
	s, err := newServer(c.Instance, c.newARES)
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

	return serveUntilStopped(ctx, s, c.drainTimeout)
}

// cachemanServer is the part of *server.CacheMan that run drives.
type cachemanServer interface {
	Printer() server.Printer
	Serve(ctx context.Context) error
	Drain(timeout time.Duration) error
	Stop() error
}

func serveUntilStopped(ctx context.Context, s cachemanServer, drainTimeout time.Duration) error {
	// Keep Serve and dagsync running while pending uploads drain on a requested stop.
	serveCtx, stopServe := context.WithCancel(context.WithoutCancel(ctx))
	defer stopServe()

	// A failed Serve also stops cacheman, so the command exits instead of waiting forever.
	grp, grpCtx := errgroup.WithContext(ctx)
	grp.Go(func() error {
		<-grpCtx.Done()
		if drainTimeout > 0 && ctx.Err() != nil {
			// Drain logs unfinished uploads. A stop with uploads left is still a clean stop
			// (exit 0) so Restart=on-failure does not restart cacheman.
			_ = s.Drain(drainTimeout)
		}
		stopServe()
		if err := s.Stop(); err != nil {
			return fmt.Errorf("failed to stop cacheman: %w", err)
		}
		return nil
	})

	grp.Go(func() error {
		err := s.Serve(serveCtx)
		if ctx.Err() != nil {
			// Stop closes the socket, which ends Serve with an accept error.
			return nil
		}
		return err
	})

	return grp.Wait()
}
