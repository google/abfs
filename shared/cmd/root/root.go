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

package root

import (
	"context"
	"fmt"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/version"
)

var flavor = instance.DefaultFlavor

type RootCmd struct {
	// A slice of all subcommands registered to this tool (e.g., run, status).
	Commands []cmd.ABFSCmd

	kvConfigs []string

	flagConfig instance.ConfigOptional

	instanceOverride         string // for the --instance flag
	instancesBaseDirOverride string // for the --abfs-base-dir flag
	showVersion              bool   // for the --version flag
}

func NewRootCommand() *RootCmd {
	return &RootCmd{}
}

func (r *RootCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	if r.showVersion {
		fmt.Println(version.Version())
		return nil
	}
	return clierr.NoSubcommandErr(fl)
}

func (r *RootCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:        instance.DefaultFlavor.Name,
		Usage:       "<command> [flags]",
		Desc:        fmt.Sprintf(`This is a CLI for the %s tool which contains many subcommands.`, flavor.Name),
		Subcommands: r.Commands,
	}
}

func (c *RootCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.instanceOverride, "instance", "i", "", fmt.Sprintf("Specify the %s instance name to use", flavor.Name))
	fl.StringVarP(&c.instancesBaseDirOverride, flavor.InstancesDirOpt, "d", "", fmt.Sprintf("Specify the %s instances root dir", flavor.Name))

	// "--kv" option provides a way to set config items from the command line without adding a new flag for each config item.
	// Till now we had to add a command-line argument for each config item we add to the abfs config.
	// And sometimes these configs are temporary or not that important to change the whole cmd-line for.
	// On the other hand, we still want to be able to specify them in the command-line to avoid editing the config file directly.
	fl.StringSliceVar(&c.kvConfigs, "kv", nil, "<key>=<value> pairs to set")
	fl.BoolVar(&c.showVersion, "version", false, "Print the application version")

	registerConfigFlags(fl, &c.flagConfig)
}

func (c *RootCmd) ResolveInstance() *instance.Instance {
	instances := instance.NewInstances(flavor)
	return instances.ResolveNewInstance(c.instanceOverride, c.instancesBaseDirOverride, &c.flagConfig, c.kvConfigs)
}
