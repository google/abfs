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
	"strings"

	"github.com/spf13/pflag"
)

func OptString(f *pflag.FlagSet, value **string, name, shorthand string, usage string) {
	flag := &pflag.Flag{
		Name:      name,
		Shorthand: shorthand,
		Usage:     usage,
		Value:     &stringPtr{value: value},
	}
	f.AddFlag(flag)
}

type stringPtr struct {
	value **string
}

func (d *stringPtr) String() string {
	if d.value == nil {
		return ""
	}
	return fmt.Sprintf("%v", *d.value)
}

func (d *stringPtr) Set(s string) error {
	if s == "" {
		*d.value = nil
		return nil
	}
	*d.value = &s
	return nil
}

func (d *stringPtr) Type() string {
	return "string"
}

func (d *stringPtr) IsBoolFlag() bool {
	return false
}

func OptStringSlice(f *pflag.FlagSet, value **[]string, name, shorthand string, usage string) {
	flag := &pflag.Flag{
		Name:      name,
		Shorthand: shorthand,
		Usage:     usage,
		Value:     &stringSlicePtr{value: value},
	}
	f.AddFlag(flag)
}

type stringSlicePtr struct {
	value **[]string
}

func (d *stringSlicePtr) String() string {
	if d.value == nil || *d.value == nil {
		return ""
	}
	out := make([]string, len(**d.value))
	for i, s := range **d.value {
		out[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(out, ",") + "]"
}

func (d *stringSlicePtr) Set(s string) error {
	if s == "" {
		*d.value = nil
		return nil
	}

	ss := strings.Split(s, ",")
	if *d.value == nil {
		*d.value = &ss
		return nil
	}
	**d.value = append(**d.value, ss...)
	return nil
}

func (d *stringSlicePtr) Type() string {
	return "stringSlice"
}

func (d *stringSlicePtr) IsBoolFlag() bool {
	return false
}
