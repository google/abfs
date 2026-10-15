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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/google/abfs/shared/entities"
)

type Servicer interface {
	Service() Service
	Name() string
}

type Service struct {
	Name        string
	Description string
	Command     string
	LogFile     string
	StdoutFile  string
}

func (s *Service) Setup(ctx context.Context) error {
	if err := s.writeServiceFile(); err != nil {
		return err
	}
	if err := s.RunAction(ctx, "enable"); err != nil {
		return fmt.Errorf("failed to enable service %q err: %w", s.Name, err)
	}
	if err := s.RunAction(ctx, "start"); err != nil {
		return fmt.Errorf("failed to start service %q err: %w", s.Name, err)
	}
	return nil
}

func (s *Service) RunAction(ctx context.Context, action string) error {
	cmd := exec.CommandContext(ctx, "systemctl", "--user", action, s.Name+".service")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// UnitFile returns the path of the service's systemd user unit file.
func (s *Service) UnitFile() string {
	return fmt.Sprintf("%s/.config/systemd/user/%s.service", os.ExpandEnv("$HOME"), s.Name)
}

func (s *Service) writeServiceFile() (retErr error) {
	serviceFile := s.UnitFile()
	if err := os.MkdirAll(path.Dir(serviceFile), 0700); err != nil {
		return err
	}

	f, err := os.OpenFile(serviceFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(f, &retErr)

	stdoutFile := s.StdoutFile
	if stdoutFile == "" {
		stdoutFile = s.LogFile
	}

	execBin := ""
	if fields := strings.Fields(s.Command); len(fields) > 0 {
		execBin = fields[0]
	}

	const serviceTemplate = `
[Unit]
Description=%s
ConditionPathExists=%s

[Service]
ExecStart=%s
StandardOutput=append:%s
StandardError=inherit
Restart=on-failure
RestartPreventExitStatus=203
RestartSec=1s
StartLimitBurst=0

[Install]
WantedBy=default.target
`
	if _, err := fmt.Fprintf(f, serviceTemplate, s.Description, execBin, s.Command, stdoutFile); err != nil {
		return err
	}
	return nil
}
