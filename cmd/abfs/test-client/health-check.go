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
	"fmt"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
)

type healthCheckCmd struct {
	*testClientCmd
	service string
}

func NewHealthCheckCmd(tc *testClientCmd) *healthCheckCmd {
	return &healthCheckCmd{testClientCmd: tc}
}

func (c *healthCheckCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "health-check",
		Usage:   "",
		Aliases: []string{},
		Desc:    `Check the health of the service`,
	}
}

func (c *healthCheckCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVar(&c.service, "service", "", "Optionally specify a service name")
}

func (c *healthCheckCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	if err := c.ABFSClient.HealthCheck(ctx, c.service); err != nil {
		return fmt.Errorf("failed to exec command: %w", err)
	}

	fmt.Printf("Service is up.\n")
	return nil
}
