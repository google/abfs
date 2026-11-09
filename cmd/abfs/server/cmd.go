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
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/pflag"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
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
	httpHost         string
	httpPort         int
	noLinkValidation bool
	noHashValidation bool
	noLogging        bool
	maxConnectionAge time.Duration
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
		Usage:       "[-p <port>] [--http-port <port>] [--no-link-validation] [--no-hash-validation] [--ares-enabled] [--max-connection-age <duration>] [--tls-cert-file <path>] [--tls-key-file <path>]",
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
	fl.StringVar(&c.httpHost, "http-host", "localhost", "The host or address the http (v1) server listens on. Its endpoints, such as /debug and /server/status, have no authentication; use an address other than localhost only on a trusted network")
	fl.IntVar(&c.httpPort, "http-port", 0, "The http (v1) server port")
	fl.DurationVar(&c.maxConnectionAge, "max-connection-age", 0, "A duration for the maximum amount of time a connection may exist before it will be closed by sending a GoAway. Supports units like '1h', '30m', '1s'. Default is 0 (no limit).")
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
		&gcpCmd{serverCmd: c},
	}
}

func (c *serverCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return clierr.NoSubcommandErr(fl)
}

func (c *serverCmd) Serve(ctx context.Context, abfsServer remote.RemoteCloser) error {
	exporter, err := prometheus.New()
	if err != nil {
		return fmt.Errorf("failed to initialize prometheus exporter: %w", err)
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	otel.SetMeterProvider(provider)

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
		ExtraServerOpts: []grpc.ServerOption{
			grpc.StatsHandler(otelgrpc.NewServerHandler()),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create grpc server: %w", err)
	}

	var wg sync.WaitGroup
	serverCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-sigChan:
			log.Printf("received interrupt signal")
		case <-serverCtx.Done():
		}
		cancel()
		s.GracefulStop()
		metricsCtx, metricsCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer metricsCancel()
		if err := provider.Shutdown(metricsCtx); err != nil {
			log.Printf("WARNING: error shutting down metrics provider: %v", err)
		}
		if aresProxy != nil {
			if err := aresProxy.Close(); err != nil {
				log.Printf("WARNING: error stopping ares proxy: %v", err)
			}
		}
	}()

	go func() {
		// This goroutine will be terminated when the main process exits.
		httpLis, err := net.Listen("tcp", net.JoinHostPort(c.httpHost, strconv.Itoa(c.httpPort)))
		if err != nil {
			log.Fatalf("Could not bind to %s port %d: %v", c.httpHost, c.httpPort, err)
		}
		log.Printf("Starting a web-server on http://%s/", httpLis.Addr())
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
		if r, ok := abfsServer.(interface{ RegisterHTTPRoutes(*mux.Router) }); ok {
			r.RegisterHTTPRoutes(router)
		}
		router.PathPrefix("/metrics").Handler(promhttp.Handler())
		if err := http.Serve(httpLis, router); err != nil {
			log.Fatalf("Error when starting or running http server: %v", err)
		}
	}()

	log.Printf("server listening at %v serving", lis.Addr())
	if err := s.Serve(lis); err != nil && err != grpc.ErrServerStopped {
		return fmt.Errorf("failed to serve: %w", err)
	}
	wg.Wait()
	return nil
}
