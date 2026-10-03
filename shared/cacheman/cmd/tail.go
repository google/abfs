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

package cacheman

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
)

type tailCmd struct {
	*cachemanCmd
	all bool
}

func NewTailCmd(c *cachemanCmd) *tailCmd {
	return &tailCmd{cachemanCmd: c}
}

func (c *tailCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "tail",
		Usage:   "[--all]",
		Aliases: []string{},
		Desc:    `Tail cacheman logs for the detected instance.`,
	}
}

func (c *tailCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.all, "all", "a", false, "When true, tail the entire file(s) without following")
}

func (c *tailCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	logPath := filepath.Join(c.Config.LogDir, "cacheman")

	currInstanceOnly := true
	cachemans, err := loadCachemans(c.Instance, currInstanceOnly)
	if err != nil {
		log.Fatal(err)
	}

	var args []string
	if c.all {
		args = append(args, "-n", "+1")
	} else {
		args = append(args, "-F")
	}
	args = append(args, logPath)
	if len(cachemans.infos) == 1 {
		// Also tail the stdout and stderr logs if they point to a real file.
		args = append(args, stdoutStderrFiles(cachemans.infos[0].pid)...)
	}
	cmd := exec.CommandContext(ctx, "tail", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// regularFilePIDFD makes best effort to return the resolved path
// to 'pid's 'fd' output file. Only regular files are considered.
func regularFilePIDFD(pid, fd int) string {
	fdPath := path.Join("/proc", strconv.Itoa(pid), "fd")
	evalPath, err := filepath.EvalSymlinks(path.Join(fdPath, strconv.Itoa(fd)))
	if err != nil {
		return ""
	}
	info, err := os.Stat(evalPath)
	if err != nil {
		return ""
	}

	if !info.Mode().IsRegular() {
		return ""
	}

	return evalPath
}

func stdoutStderrFiles(pid int) []string {
	stdoutPath := regularFilePIDFD(pid, syscall.Stdout)
	stderrPath := regularFilePIDFD(pid, syscall.Stderr)

	var files []string
	if stdoutPath != "" {
		files = append(files, stdoutPath)
	}

	if stdoutPath != stderrPath && stderrPath != "" {
		files = append(files, stderrPath)
	}

	return files
}
