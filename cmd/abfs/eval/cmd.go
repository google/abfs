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

package eval

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"

	"github.com/spf13/pflag"

	"github.com/google/abfs/eval"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

const tapNoCGo = "tap-no-cgo"

type evalCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	*cmd.StoreProvider
	*cmd.CachemanProvider
	reqHash         entities.Hash
	patchHashesStr  []string
	patchHashes     []entities.Hash
	keep            bool
	writeResp       bool
	showRunTimeInfo bool
}

func NewCmd(rootCmd *root.RootCmd) *evalCmd {
	return &evalCmd{RootCmd: rootCmd}
}

func (c *evalCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "eval",
		Usage: "[flags] [-p <patch hash>,<patch hash>] <eval-request-hash>",
		Desc:  `Runs an eval request against its included inputs`,
	}
}

func (c *evalCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.keep, "keep", "k", false, "Do not umount and remove the mount point")
	fl.BoolVarP(&c.writeResp, "write", "w", false, "Write the response object to the cache (otherwise it's printed)")
	fl.BoolVar(&c.showRunTimeInfo, "run-info", false, "Display runtime information")
	fl.StringSliceVarP(&c.patchHashesStr, "patches", "p", nil, "Patches that should be applied before evaluating the request")
}

func (c *evalCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	reqHash := args.EnsureSingleArg(fl)
	if !entities.IsHash(reqHash) {
		return clierr.CLIErr("Provided eval-request hash %q was not valid", reqHash)
	}
	c.reqHash = entities.NewHash(reqHash)

	if len(c.patchHashesStr) > 0 {
		c.patchHashes = make([]entities.Hash, 0, len(c.patchHashesStr))
		for _, s := range c.patchHashesStr {
			if !entities.IsHash(s) {
				return clierr.CLIErr("One of the provided patch hashes %q was not valid", s)
			}
			h := entities.NewHash(s)
			c.patchHashes = append(c.patchHashes, h)
		}
	}

	return c.run(ctx)
}

func (c *evalCmd) run(ctx context.Context) error {
	localTap, err := exec.LookPath(tapNoCGo)
	if err != nil {
		return fmt.Errorf("binary %q not found in PATH", tapNoCGo)
	}
	evaluator, err := eval.NewEvaluator(c.Store, c.Cacheman, eval.SetupInput{
		Keep:    c.keep,
		TapPath: localTap,
		ABFSConfig: eval.ABFSConfig{
			FSType:       c.Config.FSType,
			LocalOnly:    c.Config.LocalOnly,
			CacheDir:     c.Config.CacheDir,
			FetchTimeout: c.Config.FetchTimeout,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create evaluator: %w", err)
	}
	defer func() {
		if err := evaluator.Close(); err != nil {
			log.Printf("WARNING: error while closing evaluator: %v", err)
		}
	}()

	resp, _, err := evaluator.Eval(ctx, c.reqHash, c.patchHashes, eval.EvalOptions{WriteEvalResponse: c.writeResp, StoreMapResult: c.writeResp})
	if err != nil {
		return fmt.Errorf("failed to Eval: %w", err)
	}
	if c.writeResp {
		log.Println("Response Hash:")
		fmt.Println(resp.Hash)
	} else {
		if err := resp.Print(os.Stdout); err != nil {
			return err
		}
	}
	if c.showRunTimeInfo {
		fmt.Printf("Duration:\n    eval %f\n", resp.RunInfo.RunDuration)
	}
	return nil
}
