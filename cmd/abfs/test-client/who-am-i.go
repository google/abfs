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

type whoAmICmd struct {
	*testClientCmd
}

func NewWhoAmICmd(tc *testClientCmd) *whoAmICmd {
	return &whoAmICmd{testClientCmd: tc}
}

func (c *whoAmICmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "who-am-i",
		Usage:   "",
		Aliases: []string{},
		Desc:    `Ask the server for the authenticated user`,
	}
}

func (c *whoAmICmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *whoAmICmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	who, err := c.ABFSClient.WhoAmI(ctx)
	if err != nil {
		return fmt.Errorf("WhoAmI failed: %w", err)
	}

	fmt.Println(who)
	return nil
}
