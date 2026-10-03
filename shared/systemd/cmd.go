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

package systemd

import (
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/root"
)

// SystemdCmd provides common subcommands for a systemd service. If you
// will provide your own subcommands, append to the `SystemdCmd.Subcommands()`
// list.
type SystemdCmd struct {
	root     *root.RootCmd
	Servicer Servicer
}

func NewSystemdCmd(rootCmd *root.RootCmd, servicer Servicer) *SystemdCmd {
	if servicer == nil {
		panic("No server specified for systemd commands")
	}
	return &SystemdCmd{root: rootCmd, Servicer: servicer}
}

func (c *SystemdCmd) Subcommands() []cmd.ABFSCmd {
	return []cmd.ABFSCmd{
		NewTailCmd(c.root, c.Servicer),
		NewActionCmd(c.root, c.Servicer, "start"),
		NewActionCmd(c.root, c.Servicer, "stop"),
		NewActionCmd(c.root, c.Servicer, "restart"),
		NewActionCmd(c.root, c.Servicer, "status"),
		NewActionCmd(c.root, c.Servicer, "disable"),
		NewActionCmd(c.root, c.Servicer, "remove"),
		NewSetupCmd(c.root, c.Servicer),
	}
}
