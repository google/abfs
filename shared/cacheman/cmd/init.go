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
	"path"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/store/localcache"
)

type initCmd struct {
	*cachemanCmd
}

func NewInitCmd(c *cachemanCmd) *initCmd {
	return &initCmd{cachemanCmd: c}
}

func (c *initCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:            "init",
		Usage:           "",
		Aliases:         []string{},
		Desc:            "Initialize cache and config",
		ProviderInjects: []any{c.SysCmd.Servicer},
	}
}

func (c *initCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *initCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	return c.init(ctx)
}

func (c *initCmd) init(ctx context.Context) error {
	inst := c.Instance
	config := inst.Config
	cfgFile := inst.ConfigFile
	if _, err := os.Stat(cfgFile); err == nil {
		fmt.Printf("Detected existing config file for instance '%s'. Existing keys will be preserved.\n", inst.Name)
	}
	cfgToWrite := c.localConfig()
	if config.WriteToConfig {
		fmt.Printf("Overwriting config for instance '%s' with provided flags...\n", inst.Name)
		cfgToWrite = inst.FlagsConfig.Clone()
	}

	// We don't want to save the 'write-to-config' flag itself into the config file.
	cfgToWrite.WriteToConfig = nil
	inst.LocalConfig = cfgToWrite.Clone()

	fmt.Printf("Writing config for instance '%s' to %s...\n", inst.Name, cfgFile)
	if err := instance.WriteConfig(cfgFile, cfgToWrite, false); err != nil {
		return err
	}

	if err := os.MkdirAll(config.LogDir, 0700); err != nil {
		return fmt.Errorf("failed to create logs directory at %s: %w", config.LogDir, err)
	}

	fmt.Printf("Creating local cache for instance '%s' in %s...\n", inst.Name, config.CacheDir)
	if err := localcache.InitLocalCache(config.CacheDir); err != nil {
		return err
	}
	if config.RBEAsCAS {
		if err := os.MkdirAll(path.Join(config.CacheDir, "identity"), 0700); err != nil {
			return fmt.Errorf("failed to create identity directory at %s: %w", path.Join(config.CacheDir, "identity"), err)
		}
	}
	if config.LocalOnly {
		if err := localcache.WriteCacheVersionIfNotPresent(config.CacheDir); err != nil {
			return err
		}
	} else if c.SysCmd.Servicer != nil {
		if SystemctlSupported() {
			fmt.Printf("Setting up %s service...\n", inst.BinaryName)
			serviceDefinition := c.SysCmd.Servicer.Service()
			if err := serviceDefinition.Setup(ctx); err != nil {
				return err
			}
		} else {
			fmt.Println("systemctl is not supported - systemd service was not setup.")
		}
	}
	fmt.Printf("Initialization for instance '%s' complete.\n", inst.Name)
	return nil
}

func (c *initCmd) localConfig() *instance.ConfigOptional {
	config := c.Instance.LocalConfig.Clone()
	config.Apply(c.Instance.FlagsConfig)
	return config
}
