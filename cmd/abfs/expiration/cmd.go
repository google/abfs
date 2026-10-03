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

package expiration

import (
	"context"
	"fmt"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/version"
)

type expirationCmd struct {
	*root.RootCmd
}

func NewCmd(rootCmd *root.RootCmd) *expirationCmd {
	return &expirationCmd{RootCmd: rootCmd}
}

func (c *expirationCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "expiration",
		Usage: "",
		Desc:  `Print the client side expiration time.`,
	}
}

func (c *expirationCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *expirationCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	if !version.HasExpiration() {
		fmt.Println("unlimited")
		return nil
	}

	exp, err := version.Expiration()
	if err != nil {
		return err
	}
	fmt.Println(exp)
	return nil
}
