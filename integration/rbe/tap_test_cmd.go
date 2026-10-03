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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/google/abfs/filesystem"
	"github.com/google/abfs/shared/cmd"
)

type tapTestCmd struct {
	*App
}

func newTapTestCmd(app *App) *tapTestCmd {
	return &tapTestCmd{
		App: app,
	}
}

func (c *tapTestCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "tap-test",
		Usage:   "[flags]",
		Aliases: []string{},
		Desc:    `Run automated tap verification test against a file tagged with ARI`,
	}
}

func (c *tapTestCmd) RegisterFlags(fl *pflag.FlagSet) {}

// tapTestCase defines an operation to test and its expected outcome.
type tapTestCase struct {
	Name     string
	GetArgs  func(targetPath string) []string
	Validate func(result *RBETapResult) error
}

func (c *tapTestCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	cannedPayload := []byte("hello from RBE tap integration test!")
	const testRelPath = "test_data/sample.txt"
	const fileMode = uint32(syscall.S_IFREG | 0644)

	meta, err := c.CreateFileWithARI(testRelPath, fileMode, cannedPayload)
	if err != nil {
		return fmt.Errorf("failed to setup test file with ARI: %w", err)
	}

	assertReadCaptured := func(result *RBETapResult) error {
		matched := c.findMatchingStat(meta.FullPath, result.Reads)
		if matched == nil {
			return fmt.Errorf("path %q was not found in tap Reads output.\nReported reads: %+v", meta.FullPath, result.Reads)
		}
		if matched.Size != meta.Size {
			return fmt.Errorf("size mismatch for %s: expected %d bytes, got %d bytes", matched.Path, meta.Size, matched.Size)
		}
		if matched.SHA256 != meta.SHA256 {
			return fmt.Errorf("hash mismatch for %s: expected %s, got %s", matched.Path, meta.SHA256, matched.SHA256)
		}
		return nil
	}
	assertWriteCaptured := func(result *RBETapResult) error {
		matched := c.findMatchingStat(meta.FullPath, result.Writes)
		if matched == nil {
			return fmt.Errorf("path %q was not found in tap Writes output.\nReported reads: %+v", meta.FullPath, result.Reads)
		}
		if matched.Size != meta.Size {
			return fmt.Errorf("size mismatch for %s: expected %d bytes, got %d bytes", matched.Path, meta.Size, matched.Size)
		}
		if matched.SHA256 != meta.SHA256 {
			return fmt.Errorf("hash mismatch for %s: expected %s, got %s", matched.Path, meta.SHA256, matched.SHA256)
		}
		return nil
	}
	assertWriteNewCaptured := func(result *RBETapResult) error {
		matched := c.findMatchingStat(meta.FullPath, result.Writes)
		if matched == nil {
			return fmt.Errorf("path %q was not found in tap Writes output.\nReported reads: %+v", meta.FullPath, result.Reads)
		}
		// Just validate its not the same data
		if matched.Size == meta.Size {
			return fmt.Errorf("size mismatch for %s: expected %d bytes, got %d bytes", matched.Path, meta.Size, matched.Size)
		}
		if matched.SHA256 == meta.SHA256 {
			return fmt.Errorf("hash mismatch for %s: expected %s, got %s", matched.Path, meta.SHA256, matched.SHA256)
		}
		return nil
	}

	tests := []tapTestCase{
		{
			Name: "cat read",
			GetArgs: func(p string) []string {
				return []string{"cat", p}
			},
			Validate: assertReadCaptured,
		},
		{
			Name: "write no change",
			GetArgs: func(p string) []string {
				return []string{"--", "sh", "-c", "true >> " + p}
			},
			Validate: assertWriteCaptured,
		},
		// NOTE we are modifying our file here
		{
			Name: "write with change",
			GetArgs: func(p string) []string {
				return []string{"--", "sh", "-c", "truncate --size 2 " + p}
			},

			Validate: assertWriteNewCaptured,
		},
	}

	var failures []string
	for _, tc := range tests {
		fmt.Printf("▶ Running: %s\n", tc.Name)

		result, err := c.runTap(ctx, tc.GetArgs(meta.FullPath))
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: execution error: %v", tc.Name, err))
			continue
		}

		if err := tc.Validate(result); err != nil {
			failures = append(failures, fmt.Sprintf("%s: validation error: %v", tc.Name, err))
			continue
		}

		fmt.Printf("  ✓ PASSED: Tap captured read event\n")
	}

	if len(failures) > 0 {
		return fmt.Errorf("%d/%d tap tests failed:\n - %s", len(failures), len(tests), strings.Join(failures, "\n - "))
	}

	fmt.Printf("\n✓ All %d tap tests passed successfully!\n", len(tests))
	return nil
}

// runTap executes tapCmd with the provided arguments and decodes the JSON output.
func (c *tapTestCmd) runTap(ctx context.Context, args []string) (*RBETapResult, error) {
	var buf bytes.Buffer
	tap := newTapCmd(c.App)
	tap.Out = &buf

	tapFlags := pflag.NewFlagSet("tap_args", pflag.ContinueOnError)
	tap.RegisterFlags(tapFlags)

	if err := tapFlags.Parse(args); err != nil {
		return nil, fmt.Errorf("failed to parse tap arguments: %w", err)
	}

	if err := tap.Exec(ctx, tapFlags); err != nil {
		return nil, fmt.Errorf("tapCmd execution failed: %w", err)
	}

	var result RBETapResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tap output: %w\nRaw output: %s", err, buf.String())
	}

	return &result, nil
}

// findMatchingStat locates a stat matching the target path by full path, relative path, or basename.
func (c *tapTestCmd) findMatchingStat(targetPath string, stats []filesystem.RBEPathStat) *filesystem.RBEPathStat {
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		absTarget = targetPath
	}
	baseTarget := filepath.Base(targetPath)

	for i, s := range stats {
		if s.Path == absTarget || s.Path == targetPath || filepath.Base(s.Path) == baseTarget {
			return &stats[i]
		}
	}
	return nil
}
