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

package nsjail

import (
	"context"
	"fmt"
	"log"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"

	"github.com/google/abfs/cmd/abfs/setup"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/optflags"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

const expectedCASCachePath = "/src/.abfs/default/cache"

type nsjailCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	nsJailBin        string
	nsJailCfg        string
	rootDir          string
	shell            bool
	chroot           string
	compiledDir      string
	lunchTarget      string
	buildTargets     []string
	buildFromSource  bool
	outDir           string
	distDir          string
	envVars          []string
	bindMounts       []string
	evalSalt         *string
	evalNoCache      *bool
	strictTappingOff *bool
}

func NewCmd(rootCmd *root.RootCmd) *nsjailCmd {
	return &nsjailCmd{RootCmd: rootCmd}
}

func (c *nsjailCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "nsjail",
		Usage:   "[-t <top-dir>] [-s] [-c <chroot-dir>] [<command-to-run>...]",
		Aliases: []string{},
		Desc: `Run the command in nsjail.

The chroot is the first existing directory listed in $ABFS_NSJAIL_CHROOT_PATHS
(colon-separated, relative to the top directory). If $ABFS_NSJAIL_CONFIG is set,
it names the NSJail build config XML (relative to the top directory) that
selects the overlays and lunch target.`,
	}
}

func (c *nsjailCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.nsJailBin, "nsjail-bin", "j", "prebuilts/build-tools/linux-x86/bin/nsjail", "The path to nsjail binary")
	fl.StringVarP(&c.nsJailCfg, "nsjail-cfg", "f", "tools/treble/build/sandbox/nsjail.cfg", "The path to nsjail configuration")
	fl.StringSliceVarP(&[]string{}, "overlays", "o", nil, "Deprecated and ignored - overlays are read from the NSJail build config ($ABFS_NSJAIL_CONFIG)")
	fl.StringVarP(&c.chroot, "chroot", "c", "chroot", "The chroot directory to create")
	fl.StringVarP(&c.rootDir, "top", "t", ".", "The path to top directory of android source")
	fl.BoolVarP(&c.shell, "shell", "s", false, "Run an interactive bash inside nsjail")
	fl.StringVar(&c.compiledDir, "compiled", "", "Use compiled binaries in this directory instead of building them")
	fl.StringVarP(&c.lunchTarget, "lunch", "l", "aosp_cf_x86_64_phone-trunk_staging-userdebug", "The 'lunch' target")
	fl.StringSliceVarP(&c.buildTargets, "build-target", "b", []string{"droid", "dist"}, "Multi-value option to choose the goal build targets")
	fl.StringSliceVarP(&c.envVars, "env", "e", []string{}, "Multi-value option to pass 'VAR=val' environment variables to nsjail")
	fl.StringSliceVarP(&c.bindMounts, "bindmount", "B", []string{}, "Multi-value option to pass '-B val' to nsjail")
	fl.BoolVar(&c.buildFromSource, "source", false, "Force rebuilding from source instead of using prebuilt binaries in $PATH")
	fl.StringVar(&c.outDir, "out", entities.OutDir, "The output directory to setup (appended to chroot if provided)")
	fl.StringVar(&c.distDir, "dist", entities.DistDir, "The dist directory to setup (appended to chroot if provided)")
	optflags.OptString(fl, &c.evalSalt, "salt", "", "Evaluation salt to use")
	optflags.OptBool(fl, &c.evalNoCache, "no-cache", "", "Do not use cache during the evaluation")
	optflags.OptBool(fl, &c.strictTappingOff, "no-strict-tap", "", "Disable strict filesystem tapping")
}

var buildScript = `
	source build/envsetup.sh &&
	lunch {{.lunch_target}} &&
	time m {{.build_goal}}`

func (c *nsjailCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	s := setup.NewCmd(c.RootCmd)

	fs := pflag.NewFlagSet("", pflag.ContinueOnError)
	if err := fs.Parse([]string{c.rootDir}); err != nil {
		log.Fatalf("could not parse setup command: %s", err)
	}

	s.ConfigProvider = c.ConfigProvider
	s.DisableCGO = true
	s.OverrideCacheDir = expectedCASCachePath
	s.CompiledDir = c.compiledDir
	s.BuildFromSource = c.buildFromSource
	s.Chroot = c.chroot
	s.EvalSalt = c.evalSalt
	s.EvalNoCache = c.evalNoCache
	s.StrictTappingOff = c.strictTappingOff

	lunchTokens := strings.Split(c.lunchTarget, "-")
	if len(lunchTokens) != 3 {
		return clierr.CLIErr("Unable to parse android target from lunch-target %s. Expected format <target>-<release>-<variant>", c.lunchTarget)
	}
	s.AndroidTarget = lunchTokens[0]
	if err := s.Exec(ctx, fs); err != nil {
		return err
	}

	nsjailBuildConfig, err := entities.LoadNSJailConfig(c.rootDir)
	if err != nil && !entities.IsNotExist(err) {
		fmt.Printf("Warning: failed to load nsjail config file: %v\n", err)
	}
	if err == nil {
		lunchableTarget := nsjailBuildConfig.LunchableAndroidTarget(lunchTokens[0])
		if lunchableTarget != "" {
			c.lunchTarget = strings.Join([]string{lunchableTarget, lunchTokens[1], lunchTokens[2]}, "-")
		}
		// TODO: Get default flags
		// TODO: Get and use default build goals if none provided
	}

	c.rootDir = path.Join(c.rootDir, c.chroot)
	cmd := strings.Join(fl.Args(), " ")
	if cmd == "" {
		cmd = strings.ReplaceAll(buildScript, "{{.build_goal}}", strings.Join(c.buildTargets, " "))
		cmd = strings.ReplaceAll(cmd, "{{.lunch_target}}", c.lunchTarget)
	} else if c.shell {
		log.Fatalf("Cannot accept command line arguments when -s/--shell is specified")
	}
	if c.shell {
		cmd = "bash --noprofile --norc"
	}
	rootDir, err := filepath.Abs(c.rootDir)
	if err != nil {
		log.Fatal(err)
	}
	c.rootDir = rootDir

	if !path.IsAbs(c.nsJailBin) {
		c.nsJailBin = path.Join(c.rootDir, c.nsJailBin)
	}
	if !path.IsAbs(c.nsJailCfg) {
		c.nsJailCfg = path.Join(c.rootDir, c.nsJailCfg)
	}
	// DIST_DIR needs to be an absolute path since commands aren't guaranteed
	// to be run from root
	c.envVars = append(c.envVars, fmt.Sprintf("DIST_DIR=%s", path.Join(entities.SrcDir, c.distDir)))
	return c.runNSJail(c.bindMounts, c.envVars, "/bin/bash", "-c", cmd)
}
