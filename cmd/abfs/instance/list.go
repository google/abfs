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

package instance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
)

type listCmd struct {
	*instanceCmd
}

func NewListCmd(instanceCmd *instanceCmd) *listCmd {
	return &listCmd{instanceCmd: instanceCmd}
}

func (c *listCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "list",
		Usage:   "",
		Aliases: []string{"ls"},
		Desc:    "List all available ABFS instances.",
	}
}

func (c *listCmd) RegisterFlags(fl *pflag.FlagSet) {}

func (c *listCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)
	return c.run()
}

func listInstances(instanceDirs string) ([]string, error) {
	entries, err := os.ReadDir(instanceDirs)
	if err != nil {
		return nil, fmt.Errorf("failed to read ABFS base directory at %q: %w", instanceDirs, err)
	}

	var instances []string
	for _, entry := range entries {
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), "instance-to-delete-") {
				// Skip the tmp dir created by "abfs instacne remove <instance>"
				continue
			}
			// Make sure the dir is a valid instance dir by checking local cache existence
			cachedir := filepath.Join(instanceDirs, entry.Name(), "cache")
			if _, err := os.Stat(cachedir); err == nil {
				instances = append(instances, entry.Name())
			}
		}
	}
	return instances, nil
}

func (c *listCmd) run() error {
	names, err := listInstances(c.Instance.BaseDir)
	if err != nil {
		return err
	}

	for _, name := range names {
		if name == c.Instance.Name {
			fmt.Printf("* %s (current)\n", name)
		} else {
			fmt.Printf("  %s\n", name)
		}
	}
	return nil
}
