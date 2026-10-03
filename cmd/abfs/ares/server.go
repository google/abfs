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

package ares

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	"github.com/google/abfs/ares/grpc/server"
	"github.com/google/abfs/ares/servers"
	"github.com/google/abfs/eval"
	"github.com/google/abfs/shared/cmd"
)

const tapNoCGo = "tap-no-cgo"
const httpShutdownTimeout = 30 * time.Second

type serverCmd struct {
	*ARESCmd
	*cmd.ConfigProvider
	*cmd.StoreProvider
	port             int
	httpPort         int
	tapPath          string
	debug            bool
	uploadTimeout    time.Duration
	maxConnectionAge time.Duration
}

func NewServerCmd(c *ARESCmd) *serverCmd {
	return &serverCmd{ARESCmd: c}
}

func (c *serverCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "server",
		Usage:   "[-p <port>] [--http-port <port>] [--tap-path <path>] [--max-connection-age <duration>]",
		Aliases: []string{},
		Desc:    `Run an ARES server.`,
	}
}

func (c *serverCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.IntVarP(&c.port, "port", "p", 50052, "The server port")
	fl.IntVar(&c.httpPort, "http-port", 0, "The http (v1) server port")
	fl.StringVar(&c.tapPath, "tap-path", "", fmt.Sprintf("Path to the tap binary. Default is emtpy string, which will look in the PATH for the binary %s", tapNoCGo))
	fl.BoolVarP(&c.debug, "debug", "d", false, "Enable debug logging")
	fl.DurationVar(&c.maxConnectionAge, "max-connection-age", 0, "A duration for the maximum amount of time a connection may exist before it will be closed by sending a GoAway. Supports units like '1h', '30m', '1s'. Default is 0 (no limit).")
	fl.DurationVar(&c.uploadTimeout, "upload-timeout", 1*time.Hour, "The timeout for uploading a response")
}

func (c *serverCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return c.run(ctx)
}

func (c *serverCmd) run(ctx context.Context) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	metrics, err := servers.MetricsForRegistration(c.Config.ManifestServer)
	if err != nil {
		return fmt.Errorf("failed to retrieve metrics: %w", err)
	}
	for _, m := range metrics {
		if err := prometheus.Register(m); err != nil {
			return fmt.Errorf("failed to register metric: %w", err)
		}
	}

	abfsBin, err := os.Executable()
	if err != nil {
		return err
	}
	localTap, err := exec.LookPath(tapNoCGo)
	if err != nil {
		return fmt.Errorf("binary %q not found in PATH", tapNoCGo)
	}
	servers.SetDebug(c.debug)
	server, err := servers.NewARESServer(abfsBin, c.Store, servers.ARESConfig{
		UploadTimeout: c.uploadTimeout,
		EvalConfig: eval.SetupInput{
			ABFSConfig: eval.ABFSConfig{
				FSType:       c.Config.FSType,
				LocalOnly:    c.Config.LocalOnly,
				CacheDir:     c.Config.CacheDir,
				FetchTimeout: c.Config.FetchTimeout,
			},
			TapPath: localTap,
			Keep:    false,
		},
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := server.Close(); err != nil {
			log.Printf("WARNING: error closing ares server: %v", err)
		}
	}()
	return c.Serve(ctx, server)
}

func (c *serverCmd) Serve(ctx context.Context, aresServer *servers.ARESServer) error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", c.port))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	// New context will be canceled if any goroutine returns an error or parent context is canceled.
	// Parent context will be canceled if OS interrupt is received
	g, ctx := errgroup.WithContext(ctx)

	s := server.NewGRPCServer(aresServer, c.maxConnectionAge)

	// handle shutdown
	g.Go(func() error {
		<-ctx.Done()
		log.Printf("Shutting down gRPC server gracefully so pending RPCs can finish...")
		s.GracefulStop()
		// lis is closed by the gRPC server so we don't need to close it
		return nil
	})

	// http debug server
	g.Go(func() error {
		// listenting on all network interfaces (not just localhost) because
		// prometheus needs to scrape our metrics
		lis, err := net.Listen("tcp", fmt.Sprintf(":%d", c.httpPort))
		if err != nil {
			return fmt.Errorf("could not bind to port %d: %w", c.httpPort, err)

		}
		port := lis.Addr().(*net.TCPAddr).Port
		log.Printf("Starting a web-server on http://localhost:%d/ (but it's available off of localhost)", port)
		router := mux.NewRouter()
		router.PathPrefix("/debug").Handler(http.DefaultServeMux)
		router.PathPrefix("/metrics").Handler(promhttp.Handler())
		var startTime = time.Now()
		router.PathPrefix("/info").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			info := struct {
				StartTime   string            `json:"StartTime"`
				MemoryUsage uint64            `json:"MemoryUsage"`
				ActiveJobs  []*eval.ActiveJob `json:"ActiveJobs"`
			}{
				StartTime:   startTime.Format(time.RFC3339),
				MemoryUsage: mem.Sys,
				ActiveJobs:  aresServer.ActiveJobs(),
			}
			if err := json.NewEncoder(w).Encode(info); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				log.Printf("Failed to encode /info response: %v", err)
			}
		})

		httpServer := &http.Server{Handler: router}
		g.Go(func() error {
			<-ctx.Done()
			log.Printf("Shutting down HTTP Server with timeout of %s...", httpShutdownTimeout)
			// new context here since outer one is already cancelled
			ctx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
			defer cancel()
			return httpServer.Shutdown(ctx)
		})

		if err := httpServer.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("error when starting or running http server: %w", err)
		}
		return nil
	})

	// gRPC server
	g.Go(func() error {
		log.Printf("gRPC Server listening at %v serving", lis.Addr())
		if err := s.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return fmt.Errorf("failed to serve: %w", err)
		}
		return nil
	})

	return g.Wait()
}
