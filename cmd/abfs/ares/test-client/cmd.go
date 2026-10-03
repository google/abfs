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

package testclient

import (
	"context"
	"log"

	"github.com/spf13/pflag"

	"github.com/google/abfs/ares/grpc/client"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
)

type aresTestClientCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	address string
}

func NewARESTestClientCmd(rootCmd *root.RootCmd) *aresTestClientCmd {
	return &aresTestClientCmd{RootCmd: rootCmd}
}

func (c *aresTestClientCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:        "test-client",
		Aliases:     []string{},
		Desc:        `Connect to an ABFS server and perform basic requests`,
		Subcommands: c.subcommands(),
	}
}

func (c *aresTestClientCmd) subcommands() []cmd.ABFSCmd {
	return []cmd.ABFSCmd{
		&executeCmd{aresTestClientCmd: c},
		&healthCheckCmd{aresTestClientCmd: c},
	}
}
func (c *aresTestClientCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVar(&c.address, "address", "", "Address of the server to connect to")
}
func (c *aresTestClientCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return clierr.NoSubcommandErr(fl)
}

func (c *aresTestClientCmd) Client() *client.ARESClient {
	addr := c.address
	if addr == "" {
		addr = c.Config.EffectiveRemoteServers()[0]
	}
	client, err := client.NewARESClient(addr, c.Config.GetAuthInfo())
	if err != nil {
		log.Fatalf("cannot connect to server %q: %v", addr, err)
	}
	return client
}
