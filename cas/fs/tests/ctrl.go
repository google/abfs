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

package tests

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/abfs/cas/fs"
)

type ctrlTest struct {
	fs fs.FS
}

func (c *ctrlTest) Control(parent, dirName string) error {
	if err := MkdirTest(parent); err != nil {
		return err
	}
	return c.fs.CreateControl(parent, dirName)
}
func (c *ctrlTest) ControlRemove(parent, dirName string) error {
	if err := c.Control(parent, dirName); err != nil {
		return err
	}
	path := filepath.Join(parent, dirName)
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("unexpected stat after delete on %s", path)
	}

	return nil
}

func (c *ctrlTest) StatusTest(ready bool) error {
	status, err := c.fs.Status()
	if err != nil {
		return err
	}
	if status.Ready != ready {
		return fmt.Errorf("expected ready %t got %t", ready, status.Ready)
	}
	return nil
}
