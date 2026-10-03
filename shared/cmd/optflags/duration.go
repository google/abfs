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

package optflags

import (
	"fmt"
	"time"

	"github.com/spf13/pflag"
)

func OptDuration(f *pflag.FlagSet, value **time.Duration, name, shorthand string, usage string) {
	flag := &pflag.Flag{
		Name:      name,
		Shorthand: shorthand,
		Usage:     usage,
		Value:     &durationPtr{value: value},
	}
	f.AddFlag(flag)
}

type durationPtr struct {
	value **time.Duration
}

func (d *durationPtr) String() string {
	if d.value == nil {
		return ""
	}
	return fmt.Sprintf("%v", *d.value)
}

func (d *durationPtr) Set(s string) error {
	if s == "" {
		*d.value = nil
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d.value = &v
	return nil
}

func (d *durationPtr) Type() string {
	return "duration"
}

func (d *durationPtr) IsBoolFlag() bool {
	return false
}
