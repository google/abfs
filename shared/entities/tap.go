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

package entities

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

type TapData struct {
	ReadPatch  Patch
	WritePatch Patch
	Deletes    DeletePatch
	TapEntries []*TapFSEntry
}

type TapCmdResult struct {
	TapData
	Pid              int
	ExitCode         int
	TapCounts        map[string]uint64
	Rusage           *syscall.Rusage
	Timestamp        time.Time
	RunDuration      float64
	SnapshotDuration float64
	Stdout           string
	Stderr           string
}

type TappingType string

const (
	TapFS      TappingType = "fs"
	TapEntries TappingType = "entries"
)

func (t *TappingType) Set(value string) error {
	switch TappingType(value) {
	case TapFS:
		*t = TapFS
	case TapEntries:
		*t = TapEntries
	default:
		return errors.New("invalid tapping type")
	}
	return nil
}

func (t *TappingType) String() string {
	return string(*t)
}

func (t *TappingType) Type() string {
	return "TappingType"
}

func TapTypes() string {
	return fmt.Sprintf("%s,%s", TapFS, TapEntries)
}
