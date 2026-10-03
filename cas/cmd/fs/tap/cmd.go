// Copyright 2025 Google LLC
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

package tap

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/cas/cmd/args"
	"github.com/google/abfs/cas/cmd/fs/root"
	"github.com/google/abfs/cas/fs"
)

type cmd struct {
	rootCmd   *root.RootCommand
	tapPath   string
	verbose   bool
	noResolve bool
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{rootCmd: rootCmd}
}
func (c *cmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:  "tap",
		Usage: "[-v] [-n] command [arguments]",
		Desc: `Run the input command while tapping FS operations.
`,
	}
}
func (c *cmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVar(&c.tapPath, "tapPath", ".ctrl/tap", "Path to tap control directory")
	fl.BoolVarP(&c.noResolve, "no resolve", "n", false, "Don't resolve write files")
	fl.BoolVarP(&c.verbose, "verbose", "v", false, "Print text verbose data")
}

func (c *cmd) showVerbose(cmd *exec.Cmd, result *fs.TapInfo) {

	userTime := float64(result.Rusage.Utime.Sec) + time.Microsecond.Seconds()*float64(result.Rusage.Utime.Usec)
	sysTime := float64(result.Rusage.Stime.Sec) + time.Microsecond.Seconds()*float64(result.Rusage.Stime.Usec)
	cpuTime := 100.0 * (float64(userTime+sysTime) / result.RunDuration)
	fmt.Printf("Command %s %s\n", cmd.Path, strings.Join(cmd.Args, " "))
	fmt.Printf("Run info:\n")
	fmt.Printf("  Build Time: %s\n", result.Timestamp)

	fmt.Printf("  Times: Duration: %s\t\tUser: %s\tSystem: %s Cpu: %f%%\n",
		time.Duration(result.RunDuration*float64(time.Second)),
		time.Duration(userTime*float64(time.Second)),
		time.Duration(sysTime*float64(time.Second)),
		cpuTime)

	fmt.Printf("  Usage Memory: %d\t\tIO in: %d\tIO out: %d\n",
		result.Rusage.Maxrss, result.Rusage.Inblock, result.Rusage.Oublock)
	fmt.Printf("  Exit Code: %d\n", result.ExitCode)
	if result.Stdout != "" {
		fmt.Println("Stdout:")
		for _, s := range strings.Split(result.Stdout, "\n") {
			fmt.Printf("| %s\n", s)
		}
		fmt.Println()
	}
	if result.Stderr != "" {
		fmt.Println("Stderr:")
		for _, s := range strings.Split(result.Stderr, "\n") {
			fmt.Printf("| %s\n", s)
		}
		fmt.Println()
	}

}
func (c *cmd) runNoResolve(cmd *exec.Cmd) {
	result, err := c.rootCmd.FS().Tap(cmd)
	if err != nil {
		log.Fatalf("tap failure: %s", err)
	}
	if c.verbose {
		c.showVerbose(cmd, &result.TapInfo)
		for _, t := range result.TapData.TapEntries {
			fmt.Println(t)
		}

	} else {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			log.Fatalf("JSON failure %s\n", err)
		}
	}

}
func (c *cmd) runResolve(cmd *exec.Cmd) {
	result, err := c.rootCmd.FS().TapResolved(cmd)
	if err != nil {
		log.Fatalf("tap failure: %s", err)
	}
	if c.verbose {
		c.showVerbose(cmd, &result.TapInfo)
		fmt.Println("Reads")
		for k, h := range result.Reads {
			if h.IsARI {
				fmt.Printf("%s ari %s %d %d\n", k, h.ARI, fs.FSModeToOSMode(h.Mode), h.Size)
			} else {
				fmt.Printf("%s hash %s %d %d\n", k, h.Hash.String(), fs.FSModeToOSMode(h.Mode), h.Size)
			}
		}
		for k, h := range result.Writes {
			fmt.Printf("%s %s %d %d\n", k, h.Hash.String(), h.Mode, h.Size)
		}
		for k, _ := range result.Deletes {
			fmt.Println(k)
		}

	} else {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			log.Fatalf("JSON failure %s\n", err)
		}
	}
}
func (c *cmd) Run(fl *pflag.FlagSet) {
	cmdArgs := args.EnsureArgs(fl)
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	// Need to tell xattr where to find control
	c.rootCmd.SetTapPath(c.tapPath)

	if c.noResolve {
		c.runNoResolve(cmd)
	} else {
		c.runResolve(cmd)
	}

}
