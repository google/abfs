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

package cacheman

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
)

type statusCmd struct {
	*cachemanCmd
}

func NewStatusCmd(c *cachemanCmd) *statusCmd {
	return &statusCmd{cachemanCmd: c}
}

func (c *statusCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "status",
		Usage:   "",
		Aliases: []string{},
		Desc:    `Display cacheman status for the detected instance.`,
	}
}

func (c *statusCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *statusCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	currInstanceOnly := true
	cachemans, err := loadCachemans(c.Instance, currInstanceOnly)
	if err != nil {
		return err
	}
	if len(cachemans.infos) != 1 {
		// systemd 'status' will exit with error if the process isn't running.
		os.Exit(1)
	}

	fmt.Printf("%s\n%s\n", cachemans.psHeader, cachemans.infos[0].psLine)
	return nil
}
