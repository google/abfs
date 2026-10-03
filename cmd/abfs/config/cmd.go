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

package config

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/editor"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/instance"
)

type configCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	asRead     bool
	remote     bool
	write      bool
	edit       bool
	noDefaults bool
	watch      bool
}

func NewCmd(rootCmd *root.RootCmd) *configCmd {
	return &configCmd{RootCmd: rootCmd}
}

func (c *configCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "config",
		Usage: "([-r] | [-w] | [-e] | [--watch] | [--remote]) [-D]",
		Desc:  `Show the effective config or optionally write/edit the config file`,
	}
}

func (c *configCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.asRead, "as-read", "r", false, "Print config as read from config file")
	fl.BoolVarP(&c.write, "write", "w", false, "Write config to the specified config file")
	fl.BoolVarP(&c.edit, "edit", "e", false, "Edit the config file")
	fl.BoolVarP(&c.remote, "remote", "", false, "Print remote config overrides")
	fl.BoolVarP(&c.watch, "watch", "", false, "Watch for changes in the config file and print the effective config")
	fl.BoolVarP(&c.noDefaults, "no-defaults", "D", false, "Don't show/write the config entries with default values")
}

func (c *configCmd) localConfig() *instance.ConfigOptional {
	config := c.Instance.LocalConfig.Clone()
	config.Apply(c.Instance.FlagsConfig)
	if !c.noDefaults {
		return config
	}
	var newConfig = *config
	newConfig.RemoveUnchanged(c.Instance.DefaultConfig)
	return &newConfig
}

func (c *configCmd) printConfig(config *instance.Config, prefix string) {
	co := config.NonZeroToOverride()
	if c.noDefaults {
		co.RemoveUnchanged(c.Instance.DefaultConfig)
	}
	buf := bytes.NewBuffer(nil)
	if err := co.EncodeHuman(buf); err != nil {
		log.Fatalf("Failed to encode config: %v", err)
	}
	for line := range strings.SplitSeq(buf.String(), "\n") {
		fmt.Println(prefix + line)
	}
}

func (c *configCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	opCount := 0
	for _, op := range []bool{c.watch, c.asRead, c.write, c.remote, c.edit} {
		if op {
			opCount++
		}
	}
	if opCount > 1 {
		return clierr.CLIErr("Only one of --watch, --as-read, --remote, --write, and --edit can be specified")
	}

	return c.run(ctx)
}

func (c *configCmd) run(ctx context.Context) error {
	switch {
	case c.remote:
		c.printConfig(c.Instance.RemoteConfig.ToConfig(), "")

	case c.watch:
		fmt.Printf("%s Current Config:\n", time.Now().Format("2006-01-02 15:04:05"))
		c.printConfig(c.Config, "  ")
		if err := c.Instance.StartWatchingConfigFile(); err != nil {
			return err
		}
		c.Instance.RegisterConfigChange(func(config *instance.Config) {
			fmt.Printf("\n%s Config updated:\n", time.Now().Format("2006-01-02 15:04:05"))
			c.printConfig(config, "  ")
		})
		<-ctx.Done()
		return ctx.Err()

	case c.asRead:
		config := c.localConfig()
		if err := config.EncodeHuman(os.Stdout); err != nil {
			return fmt.Errorf("failed to encode config: %v", err)
		}

	case c.write:
		config := c.localConfig()
		if err := instance.WriteConfig(c.Instance.ConfigFile, config, false); err != nil {
			return fmt.Errorf("failed to write config: %v", err)
		}
		fmt.Printf("Wrote config to %q.\n", c.Instance.ConfigFile)

	case c.edit:
		return c.editConfig()

	default:
		c.printConfig(c.Config, "")
	}
	return nil
}

func (c *configCmd) editConfig() error {
	configFile := c.Instance.ConfigFile
	configDir := filepath.Dir(configFile)

	vimSpecs := editor.BuildStructVimSpecs[instance.Config]()

	err := editor.EditWithValidation(editor.EditOptions{
		TargetFile:    configFile,
		TempDir:       configDir,
		TempPrefix:    filepath.Base(configFile) + ".",
		FileExtension: ".yaml",
		RejectedFile:  configFile + ".rejected",
		Filetype:      "yaml",
		VimSpecs:      vimSpecs,
		Validate: func(tmpPath string) error {
			return validateEditedConfig(tmpPath, c.Instance)
		},
	})
	if err != nil {
		return err
	}

	fmt.Printf("Wrote config to %q.\n", configFile)
	return nil
}

func validateEditedConfig(tmpPath string, inst *instance.Instance) error {
	editedConfig, err := instance.ReadConfig[*instance.ConfigOptional](tmpPath)
	if err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	effectiveConfig := inst.DefaultConfig.Clone()
	effectiveConfig.Apply(inst.RemoteConfig)
	effectiveConfig.Apply(editedConfig)
	effectiveConfig.Apply(inst.FlagsConfig)
	if err := effectiveConfig.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return nil
}
