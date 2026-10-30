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

package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"time"

	"github.com/gorilla/mux"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	aresgrpc "github.com/google/abfs/ares/grpc"
	arespb "github.com/google/abfs/ares/grpc/proto"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
	abfsgrpc "github.com/google/abfs/shared/remote/grpc"
	"github.com/google/abfs/shared/remote/grpc/server"
	"github.com/google/abfs/shared/remote/servers"
)

type serverCmd struct {
	*root.RootCmd
	port             int
	httpPort         int
	noLinkValidation bool
	noHashValidation bool
	noLogging        bool
	maxConnectionAge time.Duration
	shutdownTimeout  time.Duration
	aresEnabled      bool
	tlsCertFile      string
	tlsKeyFile       string
	aclEnabled       bool
	aclFile          string
	authEnabled      bool
	logSampleRate    float64
}

func NewCmd(rootCmd *root.RootCmd) *serverCmd {
	return &serverCmd{RootCmd: rootCmd}
}

func (c *serverCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:        "server",
		Usage:       "[-p <port>] [--http-port <port>] [--no-link-validation] [--no-hash-validation] [--ares-enabled] [--max-connection-age <duration>] [--shutdown-timeout <duration>] [--tls-cert-file <path>] [--tls-key-file <path>]",
		Aliases:     []string{},
		Desc:        `Start ABFS server for blobs, objects and map results.`,
		Subcommands: c.subcommands(),
	}
}

func (c *serverCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.noLinkValidation, "no-link-validation", "s", false, "Skip validating object links")
	fl.BoolVarP(&c.noHashValidation, "no-hash-validation", "c", false, "Skip validating checksum of objects")
	fl.BoolVarP(&c.noLogging, "no-logging", "L", false, "Skip logging of requests")
	fl.IntVarP(&c.port, "port", "p", 50051, "The server port")
	fl.IntVar(&c.httpPort, "http-port", 0, "The http (v1) server port")
	fl.DurationVar(&c.maxConnectionAge, "max-connection-age", 0, "A duration for the maximum amount of time a connection may exist before it will be closed by sending a GoAway. Supports units like '1h', '30m', '1s'. Default is 0 (no limit).")
	fl.DurationVar(&c.shutdownTimeout, "shutdown-timeout", 25*time.Second, "How long to wait for pending RPCs on shutdown before closing their connections, so that a stalled or vanished client cannot keep the server from exiting. Keep it below the time your process manager waits before killing the server. 0 means no limit.")
	// TODO: override hashpath or some other solution for local testing?
	fl.BoolVar(&c.aresEnabled, "ares-enabled", false, fmt.Sprintf("Polls the hashpath %q for the list of ARES servers to proxy to", aresgrpc.ConfigARESServersHashPath))
	fl.StringVar(&c.tlsCertFile, "tls-cert-file", "", "Path to the TLS certificate file")
	fl.StringVar(&c.tlsKeyFile, "tls-key-file", "", "Path to the TLS key file")
	fl.BoolVar(&c.aclEnabled, "acl-enabled", false, "Enable reference authorization")
	fl.StringVar(&c.aclFile, "acl-file", "", "Path to a JSON file containing initial ACLs to set on the server")
	fl.BoolVar(&c.authEnabled, "auth-enabled", false, "Enable user authentication interceptors")
	fl.Float64Var(&c.logSampleRate, "log-sample-rate", 1.0, "Logging sampling rate for successfull calls (0 means no logging and 1 means all)")
}

func (c *serverCmd) subcommands() []cmd.ABFSCmd {
	return []cmd.ABFSCmd{
		&inMemoryCmd{serverCmd: c},
		&fileBasedCmd{serverCmd: c},
	}
}

func (c *serverCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return clierr.NoSubcommandErr(fl)
}

func (c *serverCmd) Serve(ctx context.Context, abfsServer remote.RemoteCloser) error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", c.port))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	var aresProxy *aresgrpc.RandomConnMgr
	var proxyMap map[string]abfsgrpc.ConnectionManager

	if c.aresEnabled {
		aresProxy = aresgrpc.NewRandomConnMgr(ctx, abfsServer)
		proxyMap = map[string]abfsgrpc.ConnectionManager{
			arespb.ARES_ServiceDesc.ServiceName: aresProxy,
		}
	}

	composedServer := abfsServer
	// Allow bootstrapping of ACLs.
	if c.aclFile != "" {
		if err := servers.LoadAndSetACLs(ctx, composedServer, c.aclFile); err != nil {
			return fmt.Errorf("failed to load and set ACLs: %w", err)
		}
	}

	if c.aclEnabled {
		store := remote.NewRemoteStore(remote.NewCompressingCloser(remote.NopCloser{RemoteProvider: composedServer}))
		aclEvaluator := remote.NewACLEvaluator(store)
		composedServer = servers.NewACLServer(composedServer, aclEvaluator)
	}
	if !c.noLogging {
		composedServer = servers.NewLoggingServer(composedServer, log.Default(), 3, c.logSampleRate)
	}
	s, err := server.NewGRPCServer(&server.GRPCServerOpts{
		Server:                   composedServer,
		ProxyPrefixToConnManager: proxyMap,
		MaxConnectionAge:         c.maxConnectionAge,
		CertFile:                 c.tlsCertFile,
		KeyFile:                  c.tlsKeyFile,
		AuthEnabled:              c.authEnabled,
	})
	if err != nil {
		return fmt.Errorf("failed to create gprc server: %w", err)
	}

	go func() {
		// This goroutine will be terminated when the main process exits.
		lis, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", c.httpPort))
		if err != nil {
			log.Fatalf("Could not bind to port %d: %v", c.httpPort, err)
		}
		port := lis.Addr().(*net.TCPAddr).Port
		log.Printf("Starting a web-server on http://localhost:%d/", port)
		router := mux.NewRouter()
		router.PathPrefix("/debug").Handler(http.DefaultServeMux)
		router.Path("/mark/{hash}/{key}/{value}").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			vars := mux.Vars(r)
			hash := entities.NewHash(vars["hash"])
			key := vars["key"]
			value := vars["value"]

			count, err := abfsServer.Mark(r.Context(), hash, key, value)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = fmt.Fprintln(w, count)
		})
		if err := http.Serve(lis, router); err != nil {
			log.Fatalf("Error when starting or running http server: %v", err)
		}
	}()

	return serveGRPC(ctx, s, lis, c.shutdownTimeout)
}

// serveGRPC serves s on lis until ctx is canceled or s fails, then stops s
// gracefully, closing the remaining connections after timeout.
func serveGRPC(ctx context.Context, s *grpc.Server, lis net.Listener, timeout time.Duration) error {
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		<-ctx.Done()
		log.Printf("Shutting down gRPC server gracefully so pending RPCs can finish...")
		// GracefulStop waits for every handler to return, also for clients
		// that stopped reading or vanished. Stop cancels their contexts, so the
		// timeout bounds the shutdown for handlers that honour them. Waiting
		// for GracefulStop keeps the backend Close after every handler.
		if timeout > 0 {
			timer := time.AfterFunc(timeout, func() {
				log.Printf("Pending RPCs did not finish within %s, closing their connections", timeout)
				s.Stop()
			})
			defer timer.Stop()
		}
		s.GracefulStop()
		return nil
	})

	g.Go(func() error {
		log.Printf("server listening at %v serving", lis.Addr())
		if err := s.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return fmt.Errorf("failed to serve: %w", err)
		}
		return nil
	})

	return g.Wait()
}
