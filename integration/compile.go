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

package integration

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
)

type Compile struct {
	SrcFile string
	tmpDir  string
	bin     string
}

func NewCompile(srcFile, tmpDir string) *Compile {
	return &Compile{SrcFile: srcFile, tmpDir: tmpDir}
}

func goMod() string {
	cmd := exec.Command("go", "env", "GOMOD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	buffer := bytes.NewBuffer(out)
	line, err := buffer.ReadString('\n')
	if err != nil {
		return ""
	}
	return strings.TrimSpace(line)
}

func (c *Compile) Compile() (string, error) {
	var err error
	c.bin, err = c.doCompile()

	return c.bin, err
}

func (c *Compile) doCompile() (string, error) {
	modPath := goMod()
	if modPath == "" {
		return "", errors.New("empty GOMOD env variable")
	}
	modDir := path.Dir(modPath)

	name := strings.ReplaceAll(c.SrcFile, "/", "-")
	outBin, err := os.CreateTemp(c.tmpDir, fmt.Sprintf("%s-bin-*", name))
	if err != nil {
		return "", err
	}
	if err = outBin.Close(); err != nil {
		return "", err
	}

	cmdStr := fmt.Sprintf("build -o %s %s", outBin.Name(), c.SrcFile)
	compileCmd := exec.Command("go", strings.Split(cmdStr, " ")...)
	compileCmd.Dir = modDir
	if err := compileCmd.Run(); err != nil {
		return "", err
	}
	return outBin.Name(), nil
}

func (c *Compile) Path() string {
	return c.bin
}

func (c *Compile) Clean() {
	_ = os.Remove(c.bin)
}
