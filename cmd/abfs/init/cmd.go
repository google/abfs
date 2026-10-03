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

package init

import (
	"context"

	"github.com/spf13/pflag"

	aresclient "github.com/google/abfs/ares/grpc/client"
	cachemancmd "github.com/google/abfs/shared/cacheman/cmd"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/remote"
)

type initCmd struct {
	*root.RootCmd
}

func NewCmd(rootCmd *root.RootCmd) *initCmd {
	return &initCmd{RootCmd: rootCmd}
}

func (c *initCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "init",
		Usage:   "",
		Aliases: []string{},
		Desc:    "Initialize ABFS cache and config",
	}
}

func (c *initCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func NewARESClient(address string, authInfo instance.AuthInfo) (remote.ARESService, error) {
	return aresclient.NewARESClientWithOptions(&aresclient.ARESClientOpts{
		Address:         address,
		AuthInfo:        authInfo,
		SkipHealthCheck: true,
	})
}

func (c *initCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	// Use the default servicer provided by the shared cacheman command.
	cachemanCmd := cachemancmd.NewCmd(c.RootCmd, NewARESClient)
	initCmd := cachemancmd.NewInitCmd(cachemanCmd)
	cmd.ABFSCmdToCli(initCmd).Run(fl)
	return nil
}
