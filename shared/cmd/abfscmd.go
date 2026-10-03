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

package cmd

import (
	"context"

	"github.com/spf13/pflag"
)

type CmdSpec struct {
	// Fields copied from cli.CommandSpec (not embedded for ease of initilization):

	// Name is the name of the command.
	// It should be the leaf name of the entire command. E.g `run` for the full
	// command `sail run`.
	Name string
	// Usage is the command's usage string.
	// E.g "[flags] <path>"
	Usage string
	// Desc is the description of the command.
	// The first line is used as an abbreviated description.
	Desc string
	// RawArgs indicates that flags should not be parsed, and they should be deferred
	// to the command.
	RawArgs bool
	// Hidden indicates that this command should not show up in it's parent's
	// subcommand help.
	Hidden bool
	// Aliases contains a list of alternative names that can be used for a particular command.
	Aliases []string

	// Extra fields:
	ProviderInjects []any

	Daemon bool

	Subcommands []ABFSCmd
}

type ABFSCmd interface {
	Spec() CmdSpec
	RegisterFlags(fl *pflag.FlagSet)
	Exec(ctx context.Context, fl *pflag.FlagSet) error
}
