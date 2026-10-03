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

package recreatebuild

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/ninjago"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

// TODO Add an option to mount the target-path
// TODO Add an option to add source
// TODO Add an option to add build-artifacts

type recreateBuildCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	*cmd.StoreProvider
	buildHash entities.Hash
	root      string
	absRoot   string
	buildInfo *binding.BuildInfo
	outDir    string
}

func NewCmd(rootCmd *root.RootCmd) *recreateBuildCmd {
	return &recreateBuildCmd{RootCmd: rootCmd}
}

func (c *recreateBuildCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "recreate-build",
		Usage:   "<build-hash> <mount-point>",
		Desc:    `Mount the source associated with the given build and put the ninja-files and logs in-place.`,
		Aliases: []string{"recreate"},
		Hidden:  true,
	}
}

func (c *recreateBuildCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *recreateBuildCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	buildHash, mntPoint := args.EnsureTwoArgs(fl)

	var err error
	c.root = mntPoint
	c.absRoot, err = filepath.Abs(c.root)
	if err != nil {
		return clierr.CLIErr("cannot get absolute path of %q: %v", c.root, err)
	}

	if !entities.IsHash(buildHash) {
		return clierr.CLIErr("%q is not a valid hash", buildHash)
	}
	c.buildHash = entities.NewHash(buildHash)

	return c.run(ctx)
}

func (c *recreateBuildCmd) run(ctx context.Context) error {
	var err error

	c.buildInfo, err = binding.FetchObject[binding.BuildInfo](ctx, c.Store, c.buildHash)
	if err != nil {
		return fmt.Errorf("cannot fetch eval-response: %w", err)
	}
	c.outDir = c.buildInfo.OutDir

	if err := c.writeNinjaConf(); err != nil {
		return err
	}
	for _, ninjaInfo := range c.buildInfo.NinjaRunInfo {
		if err := c.fetchNinja(ninjaInfo); err != nil {
			return err
		}
	}
	if err := c.putOutFile(".ninja_log", entities.NewHash(c.buildInfo.NinjaLog)); err != nil {
		return err
	}
	if err := c.putOutFile(".wrap_log", entities.NewHash(c.buildInfo.WrapLog)); err != nil {
		return err
	}
	return nil
}

func (c *recreateBuildCmd) writeNinjaConf() error {
	ninjaConf := &ninjago.Configuration{
		CacheDir: c.Config.CacheDir,
	}
	return ninjaConf.Write(path.Join(c.root, ninjago.ConfigFile))
}

func (c *recreateBuildCmd) fetchNinja(ninjaInfo binding.NinjaRunInfo) error {
	for f, h := range ninjaInfo.BuildsFiles {
		if err := c.putFile(f, entities.NewHash(h)); err != nil {
			return err
		}
	}
	return nil
}

func (c *recreateBuildCmd) putOutFile(p string, h entities.Hash) error {
	return c.putFile(path.Join(c.outDir, p), h)
}

func (c *recreateBuildCmd) putFile(p string, h entities.Hash) error {
	if h.IsZero() {
		return nil
	}

	fmt.Printf("putFile %q with hash %s\n", p, h)

	// To guard against path traversal attacks
	p = path.Join(c.root, p)
	if !strings.HasPrefix(p+"/", c.absRoot+"/") {
		return fmt.Errorf("%q is not inside root %q", p, c.absRoot)
	}

	dirName := path.Dir(p)
	if err := os.MkdirAll(dirName, 0755); err != nil {
		return fmt.Errorf("cannot create dir %q: %w", dirName, err)
	}
	if err := os.WriteFile(p, nil, 0644); err != nil {
		return err
	}
	return bindcas.SetHash(p, h)
}
