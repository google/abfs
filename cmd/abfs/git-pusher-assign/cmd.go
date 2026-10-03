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

package gitpusherassign

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"

	"github.com/google/abfs/third_party/rendezvous"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/root"
)

type gitPusherAssignCmd struct {
	*root.RootCmd
	numWorkers int
}

func NewCmd(rootCmd *root.RootCmd) *gitPusherAssignCmd {
	return &gitPusherAssignCmd{RootCmd: rootCmd}
}

func (c *gitPusherAssignCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "git-pusher-assign",
		Usage:   "",
		Aliases: []string{},
		Hidden:  true,
		Desc:    `Show worker assignment given a list of {server}/{project name} line delimited items through stdin.`,
	}
}

func (c *gitPusherAssignCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.IntVarP(&c.numWorkers, "num-workers", "n", 3, "Number of workers")
}

func (c *gitPusherAssignCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	hasher := rendezvous.New()
	for i := range c.numWorkers {
		hasher.Add(fmt.Sprintf("worker-%d", i))
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		project := scanner.Text()
		trimmed := strings.TrimSpace(project)
		if trimmed == "" {
			continue
		}
		fmt.Printf("%s: %s\n", trimmed, hasher.Get(trimmed))
	}

	return scanner.Err()
}
