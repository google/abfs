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

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"go.coder.com/cli"

	cachemancmd "github.com/google/abfs/shared/cacheman/cmd"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/systemd"
)

func main() {
	r := root.NewRootCommand()

	instanceAwareServicer := &servicer{RootCmd: r}
	cachemanCmds := cachemancmd.NewCmdWithServicer(r, nil, instanceAwareServicer)
	spec := cachemanCmds.Spec()
	subcommands := spec.Subcommands
	if systemctlSupported() {
		subcommands = append(subcommands, cachemanCmds.SysCmd.Subcommands()...)
	}
	r.Commands = subcommands

	cli.Run(cmd.ABFSCmdToCli(r), os.Args[1:], "")
}

func systemctlSupported() bool {
	return exec.Command("systemctl", "--user").Run() == nil
}

// servicer is an empty wrapper struct just to return the cacheman service
type servicer struct {
	*root.RootCmd
	*cmd.ConfigProvider
}

var _ systemd.Servicer = (*servicer)(nil)

func (s servicer) Name() string {
	return "Cacheman"
}

func (s servicer) Service() systemd.Service {
	executable, err := os.Executable()
	if err != nil {
		panic(fmt.Sprintf("Could not determine current executable path: %v", err))
	}
	config := s.Config
	cachemanLogFile := filepath.Join(config.LogDir, "cacheman")

	instanceName := s.Instance.Name
	// Construct the command with the --instance flag so the daemon knows which instance it is.
	command := fmt.Sprintf("%s --instance %s --%s %s run --logfile %s",
		executable,
		instanceName,
		instance.DefaultFlavor.InstancesDirOpt,
		s.Instance.BaseDir,
		cachemanLogFile,
	)
	return systemd.Service{
		// The service name is unique per instance (e.g., "cacheman@default", "cacheman@other").
		Name:        fmt.Sprintf("cacheman@%s", instanceName),
		Description: fmt.Sprintf("The Cacheman service for instance '%s'", instanceName),
		Command:     command,
		LogFile:     cachemanLogFile,
		StdoutFile:  filepath.Join(config.LogDir, "cachemand"),
	}
}
