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

package license

import (
	"context"
	"embed"
	_ "embed"
	"fmt"
	"io/fs"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/root"
)

//go:embed *
var embeddedFiles embed.FS
var licenseTxt string

// We use embed.FS to allow compilation in the event the LICENSE
// file is not present.
func init() {
	_, err := fs.Stat(embeddedFiles, "LICENSE")
	if err == nil {
		b, err := fs.ReadFile(embeddedFiles, "LICENSE")
		if err != nil {
			panic("Cannot read embedded LICENSE file")
		}
		licenseTxt = string(b)
	}
}

type licenseCmd struct {
	*root.RootCmd
}

func NewCmd(rootCmd *root.RootCmd) *licenseCmd {
	return &licenseCmd{RootCmd: rootCmd}
}

func (c *licenseCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "license",
		Usage:   "",
		Aliases: []string{},
		Desc:    `Display the ABFS License.`,
	}
}

func (c *licenseCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *licenseCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	fmt.Println(licenseTxt)
	return nil
}
