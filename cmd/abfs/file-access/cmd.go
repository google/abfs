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

package hash

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"

	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/ninjago/ninjalog"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

type PatchEntryStates []entities.PatchEntryState

func (p *PatchEntryStates) String() string {
	var out string
	for i, v := range *p {
		if i == 0 {
			out = v.String()
		} else {
			out = fmt.Sprintf("%s, %s", out, v)
		}
	}
	return out
}
func (p *PatchEntryStates) Set(value string) error {
	vals := strings.Split(value, ",")
	for _, s := range vals {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		pe, err := entities.PatchEntryStateByName(s)
		if err != nil {
			return err
		}
		*p = append(*p, pe)
	}
	return nil
}
func (p *PatchEntryStates) Type() string {
	return "PatchEntryStates"
}

type fileAccessCmd struct {
	*root.RootCmd
	*cmd.StoreProvider
	ninjaLogFile string
	wrapLogFile  string
	patterns     []string
	files        []string
	patchStates  PatchEntryStates
	showReads    bool
	showWrites   bool
	showDeletes  bool
	filesMap     map[string]bool
	matcher      *regexp.Regexp
}

func NewCmd(rootCmd *root.RootCmd) *fileAccessCmd {
	return &fileAccessCmd{RootCmd: rootCmd}
}

func (c *fileAccessCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "file-access",
		Usage:   "[-n <ninja-log>] [-g regexp...] [-r] [-w] [-d] [-s patch-state...] [-i patch-info] [<file-name>...]",
		Aliases: []string{},
		Desc:    `Given a source or build artifact print all the build targets interacting with that file`,
		Hidden:  true,
	}
}

func (c *fileAccessCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.ninjaLogFile, "ninja-log", "n", entities.OutDir+"/.ninja_log", "Path to the ninjago log file")
	fl.StringVar(&c.wrapLogFile, "wrap-log", entities.OutDir+"/.wrap_log", "Path to the wrap log file")
	fl.StringArrayVarP(&c.patterns, "grep", "g", nil, "Regular expression to match filename")
	fl.VarP(&c.patchStates, "patch-state", "s", "Patch states to match")
	fl.BoolVarP(&c.showReads, "reads", "r", false, "Show read accesses")
	fl.BoolVarP(&c.showWrites, "writes", "w", false, "Show write accesses")
	fl.BoolVarP(&c.showDeletes, "deletes", "d", false, "Show delete or rename accesses")
}

func (c *fileAccessCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	if fl.NArg() == 0 && len(c.patterns) == 0 && len(c.patchStates) == 0 {
		return clierr.CLIUsage(fl, "No files, patterns or patch-states specified.")
	}

	c.files = fl.Args()

	if !c.showReads && !c.showWrites && !c.showDeletes {
		c.showReads = true
		c.showWrites = true
		c.showDeletes = true
	}

	return c.run(ctx)
}

func parseLog(fileName string) (entries []ninjalog.NinjaLogEntries, retErr error) {
	f, err := os.Open(fileName)
	if err != nil {
		return nil, err
	}
	defer func() { retErr = entities.FirstError(retErr, f.Close()) }()
	return ninjalog.Parse(f)
}

func (c *fileAccessCmd) run(ctx context.Context) error {
	c.filesMap = make(map[string]bool, len(c.files))
	for _, f := range c.files {
		c.filesMap[f] = true
	}

	if len(c.patterns) > 0 {
		sb := new(strings.Builder)
		for _, p := range c.patterns {
			if sb.Len() > 0 {
				sb.WriteByte('|')
			}
			sb.WriteString(p)
		}

		c.matcher = regexp.MustCompile(sb.String())
	}

	var logs []ninjalog.NinjaLogEntries
	if c.ninjaLogFile != "" {
		parsed, err := parseLog(c.ninjaLogFile)
		if err != nil {
			return err
		}
		logs = parsed
	}
	if c.wrapLogFile != "" {
		parsed, err := parseLog(c.wrapLogFile)
		if err != nil {
			return err
		}
		logs = append(logs, parsed...)
	}

	g := new(errgroup.Group)
	g.SetLimit(runtime.NumCPU())
	for _, l := range logs {
		for i, e := range l.Entries {
			if i > 0 && l.Entries[i-1].RespHash == e.RespHash {
				continue
			}
			g.Go(func() error {
				return c.processResp(ctx, e.RespHash)
			})
		}
	}
	if err := g.Wait(); err != nil {
		return err
	}
	return nil
}
func (c *fileAccessCmd) patchMatch(p *entities.PatchEntry) bool {

	for _, k := range c.patchStates {
		if p.State == k {
			return true
		}
	}
	return false

}
func (c *fileAccessCmd) processResp(ctx context.Context, respHash entities.Hash) error {
	obj, err := c.Store.GetObject(ctx, respHash)
	if err != nil {
		return err
	}
	_, respObj, err := objects.Factory.Decode(respHash, obj)
	if err != nil {
		return err
	}
	showIt := func(reqHash, respHash entities.Hash, name string, p string) { //*entities.PatchEntry) {
		fmt.Printf("%s %s %6s %s\n", reqHash, respHash, name, p)
	}
	resp := respObj.(*binding.EvalResponse)
	if c.showReads {
		for _, p := range resp.ReadPatch {
			if c.filesMap[p.Path] || c.matcher != nil && c.matcher.MatchString(p.Path) || c.patchMatch(p) {
				showIt(resp.EvalRequest, respHash, "read", p.Path)
			}
		}
	}
	if c.showWrites {
		for _, p := range resp.WritePatch {
			if c.filesMap[p.Path] || c.matcher != nil && c.matcher.MatchString(p.Path) || c.patchMatch(p) {
				showIt(resp.EvalRequest, respHash, "write", p.Path)
			}
		}
	}
	if c.showDeletes {
		for _, p := range resp.Deletes {
			if c.filesMap[p] || c.matcher != nil && c.matcher.MatchString(p) {
				showIt(resp.EvalRequest, respHash, "delete", p)
			}
		}
	}
	return nil
}
