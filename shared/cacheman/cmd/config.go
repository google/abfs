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
)

type configCmd struct {
	*cachemanCmd
	*cmd.CachemanProvider
}

func NewConfigCmd(c *cachemanCmd) *configCmd {
	return &configCmd{cachemanCmd: c}
}

func (c *configCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "config",
		Usage: "",
		Desc: `Display the config of the running cacheman process.

This does not interact with the existing abfs config.
It is also a read-only action and does not mutate the active cacheman state.`,
	}
}

func (c *configCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *configCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	readConfig, err := c.Cacheman.ReadConfig()
	if err != nil {
		return err
	}
	if err := readConfig.Encode(os.Stdout); err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}
	return nil
}
