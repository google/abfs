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

package mountclient

import (
	"context"
	"fmt"
	"log"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/entities"
)

type providerPingCmd struct {
	*mountClientCmd
}

func newProviderPingCmd(c *mountClientCmd) *providerPingCmd {
	return &providerPingCmd{mountClientCmd: c}
}

func (c *providerPingCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "provider-ping",
		Usage:   "[<mount-point>]",
		Aliases: []string{},
		Desc:    `Send a provider ping request to the running ABFS daemon.`,
	}
}

func (c *providerPingCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *providerPingCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	mountPoint := args.SingleArgOrDefault(fl, ".")

	client, err := binding.NewClient(mountPoint, entities.ABFSSocketFileName, c.Config.CacheDir, c.Config.LocalOnly, c.Config.FetchTimeout)
	if err != nil {
		return fmt.Errorf("failed to connect to ABFS daemon at %q: %w", mountPoint, err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			log.Printf("Error closing abfs client: %v", err)
		}
	}()

	if err := client.ProviderPing(); err != nil {
		return err
	}

	fmt.Println("Provider ping successful.")
	return nil
}
