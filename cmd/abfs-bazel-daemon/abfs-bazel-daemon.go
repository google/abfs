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

package main

import (
	"fmt"
	"log"
	"net"
	"os"

	"github.com/spf13/pflag"
	"go.coder.com/cli"
	"google.golang.org/grpc"

	"github.com/google/abfs/bazeldaemon"
	"github.com/google/abfs/binding"
	"github.com/google/abfs/ninjago"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/version"
	"github.com/google/abfs/store"
)

type cmd struct {
	protocol    string
	address     string
	buildOut    string
	nsjail      bool
	forceRun    bool
	ninjaConfig *ninjago.Configuration

	showVersion bool

	store  entities.CASStore
	client *binding.Client
}

func NewCmd() *cmd {
	return &cmd{
		ninjaConfig: ninjago.LoadConfig(),
	}
}

func (c *cmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:    "bazel-daemon",
		Usage:   "[-f] [-p port] [-b <build-out>] [<CASFS-root>]",
		Aliases: []string{},
		Desc:    `Start a bazel remote execution daemon`,
	}
}

func (c *cmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.protocol, "protocol", "p", "tcp", "The protocol for the grpc server")
	fl.StringVarP(&c.address, "address", "a", "localhost:5823", "The listening address for the grpc server")
	fl.StringVarP(&c.buildOut, "build-out", "b", ".casfs-build", "The root of the build-out directory")
	fl.BoolVarP(&c.nsjail, "nsjail", "n", false, "Whether the daeamon is running inside nsjail")
	fl.BoolVarP(&c.forceRun, "force-run", "f", false, "Ignore cache hits and force build")
	fl.BoolVar(&c.showVersion, "version", false, "Print the application version")
}

func (c *cmd) Run(fl *pflag.FlagSet) {
	store, err := store.NewCASStore(c.ninjaConfig.CacheDir, c.ninjaConfig.LocalOnly, c.ninjaConfig.FetchTimeout)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("Failed to close store: %v", err)
		}
	}()
	c.store = store

	var root = "."
	if len(fl.Args()) > 0 {
		root = args.EnsureSingleArg(fl)
	}
	c.client, err = binding.NewClient(root, entities.ABFSSocketFileName, c.ninjaConfig.CacheDir, c.ninjaConfig.LocalOnly, c.ninjaConfig.FetchTimeout)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := c.client.Close(); err != nil {
			log.Printf("Failed to close client: %v", err)
		}
	}()

	if err := c.run(root); err != nil {
		log.Fatal(err)
	}
}

func (c *cmd) run(root string) error {
	lis, err := net.Listen(c.protocol, c.address)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	gopts := []grpc.ServerOption{
		// Increase gRPC message size.
		grpc.MaxRecvMsgSize(1024 * 1024 * 8),
	}

	grpcServer := grpc.NewServer(gopts...)
	server := bazeldaemon.NewServer(c.store, c.client, root, c.buildOut, c.nsjail, c.forceRun)
	server.Register(grpcServer)

	log.Printf("server listening at %v", lis.Addr())
	if err := grpcServer.Serve(lis); err != nil {
		return err
	}
	return nil
}

func main() {
	fl := pflag.NewFlagSet(os.Args[0], pflag.ExitOnError)
	c := NewCmd()
	fl.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] [<CASFS-root>]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Description: \n")
		fl.PrintDefaults()
		os.Exit(1)
	}

	c.RegisterFlags(fl)

	if err := fl.Parse(os.Args[1:]); err != nil {
		log.Fatal(err)
	}

	if c.showVersion {
		fmt.Println(version.Version())
		return
	}

	c.Run(fl)
}
