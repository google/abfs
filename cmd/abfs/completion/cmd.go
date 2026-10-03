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

package completion

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
)

type completionCmd struct {
	*root.RootCmd
}

func NewCmd(rootCmd *root.RootCmd) *completionCmd {
	return &completionCmd{RootCmd: rootCmd}
}

func (c *completionCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "completion",
		Usage:   `completion "$cur" "$cword" "${words[@]:1}")`,
		Aliases: []string{},
		Hidden:  true,
		Desc:    `Generates bash completion output`,
		RawArgs: true,
	}
}

func (c *completionCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *completionCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return c.run(fl)
}

func (c *completionCmd) run(fl *pflag.FlagSet) error {
	args := fl.Args()
	if len(args) == 0 {
		fmt.Println(`
_abfs_completion() {
    local cur prev words cword
    _get_comp_words_by_ref -n : cur prev words cword
    local IFS=$'\n'
    local completions
    completions="$("${words[0]}" completion "$cur" "$cword" "${words[@]:1}")"
    COMPREPLY=($(compgen -W "$completions" -- "$cur"))
}

complete -o nospace -F _abfs_completion abfs

# Run the following to enable bash auto-compeletion for abfs:
# . <(abfs completion)
		`)
		return nil
	}

	if len(args) < 2 {
		fl.Usage()
		return clierr.CLIErr("not enough arguments")
	}

	cur := args[0]
	cwordText := args[1]
	words := args[2:]

	cword, err := strconv.Atoi(cwordText)
	if err != nil || cword < 0 || cword > len(words) {
		fl.Usage()
		return err
	}
	words = words[:cword-1]

	result, post, err := autoComplete(c.RootCmd, words)
	if err != nil {
		return err
	}

	for _, w := range result {
		if strings.HasPrefix(w, cur) && strings.HasPrefix(cur, "-") == strings.HasPrefix(w, "-") {
			fmt.Println(w + " ")
		}
	}

	if post == postProcessOnlyGlob || (cur != "" && post == postProcessNone) {
		for _, w := range glob(cur) {
			fmt.Println(w)
		}
	}

	if cur == "" && post == postProcessDefault {
		fmt.Println("./")
		fmt.Println("-")
	}

	return nil
}

func glob(cur string) []string {
	dir, base := path.Split(cur)
	if dir != "" && !strings.HasSuffix(dir, string(filepath.Separator)) {
		dir += string(filepath.Separator)
	}

	actualDir := dir
	if actualDir == "" {
		actualDir = "."
	}
	ents, err := os.ReadDir(actualDir)
	if err != nil { // ignore the error
		return nil
	}

	words := make([]string, 0)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), base) {
			if e.IsDir() {
				words = append(words, dir+e.Name()+string(filepath.Separator))
			} else {
				words = append(words, dir+e.Name()+" ")
			}
		}
	}
	return words
}

func subCommands(c cmd.ABFSCmd, includeHiddens bool) []cmd.ABFSCmd {
	var cmds []cmd.ABFSCmd
	for _, child := range c.Spec().Subcommands {
		if includeHiddens || !child.Spec().Hidden {
			cmds = append(cmds, child)
		}
	}
	return cmds
}

type postProcess int

const (
	postProcessDefault postProcess = iota
	postProcessNone
	postProcessOnlyGlob
)

type BoolFlag interface {
	IsBoolFlag() bool
}

func isSingleValueFlag(flag *pflag.Flag) bool {
	if boolFlag, ok := flag.Value.(BoolFlag); ok && boolFlag.IsBoolFlag() {
		return true
	}
	return false
}

type ChoicesFlag interface {
	Choices() []string
}

func autoComplete(cmd cmd.ABFSCmd, words []string) ([]string, postProcess, error) {
	cmd, rest := lastCmd(cmd, words)

	if len(rest) > 0 {
		lastWord := rest[len(rest)-1]
		if strings.HasPrefix(lastWord, "-") {
			flag := lookup(cmd, lastWord)
			if flag != nil {
				if !isSingleValueFlag(flag) {
					if args.IsPath(flag) {
						return nil, postProcessOnlyGlob, nil
					}
					switch n := flag.Value.(type) {
					case ChoicesFlag:
						return n.Choices(), postProcessNone, nil
					default:
					}
				}
			}
		}
	}

	result := flags(cmd)
	for _, c := range subCommands(cmd, false) {
		if !c.Spec().Hidden {
			result = append(result, c.Spec().Name)
		}
	}

	return result, postProcessDefault, nil
}

func lastCmd(c cmd.ABFSCmd, words []string) (cmd.ABFSCmd, []string) {
	var subCmd cmd.ABFSCmd
	for {
		subCmd, words = routeCmd(c, words)
		if subCmd == c {
			return subCmd, words
		}
		c = subCmd
	}
}

func routeCmd(cmd cmd.ABFSCmd, words []string) (cmd.ABFSCmd, []string) {
	for i, w := range words {
		if strings.HasPrefix(w, "-") {
			continue
		}
		for _, c := range subCommands(cmd, true) {
			if c.Spec().Name == words[0] {
				return c, words[i+1:]
			}
			if slices.Contains(c.Spec().Aliases, words[0]) {
				return c, words[i+1:]
			}
		}
	}
	return cmd, words
}

func lookup(cmd cmd.ABFSCmd, opt string) *pflag.Flag {
	fl := pflag.NewFlagSet("", pflag.ContinueOnError)
	cmd.RegisterFlags(fl)
	if strings.HasPrefix(opt, "--") {
		return fl.Lookup(opt[2:])
	}
	if strings.HasPrefix(opt, "-") {
		opt = opt[1:]
		if len(opt) == 0 {
			return nil
		}
		return fl.ShorthandLookup(opt[len(opt)-1:])
	}
	return nil
}

func flags(cmd cmd.ABFSCmd) []string {
	fl := pflag.NewFlagSet("", pflag.ContinueOnError)
	cmd.RegisterFlags(fl)
	flags := []string{"--help"}
	fl.VisitAll(func(f *pflag.Flag) {
		flags = append(flags, "--"+f.Name)
		if len(f.Shorthand) > 0 {
			flags = append(flags, "-"+f.Shorthand)
		}
	})
	return flags
}
