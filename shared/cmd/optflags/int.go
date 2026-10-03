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
	"strconv"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/spf13/pflag"
)

func OptUint64(f *pflag.FlagSet, value **uint64, name, shorthand string, usage string) {
	flag := &pflag.Flag{
		Name:      name,
		Shorthand: shorthand,
		Usage:     usage,
		Value:     &uint64Ptr{value: value},
	}
	f.AddFlag(flag)
}

type uint64Ptr struct {
	value **uint64
}

func (d *uint64Ptr) String() string {
	if d.value == nil {
		return ""
	}
	return fmt.Sprintf("%v", *d.value)
}

func (d *uint64Ptr) Set(s string) error {
	if s == "" {
		*d.value = nil
		return nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return err
	}
	*d.value = &v
	return nil
}

func (d *uint64Ptr) Type() string {
	return "uint64"
}

func (d *uint64Ptr) IsBoolFlag() bool {
	return false
}

func OptSize(f *pflag.FlagSet, value **uint64, name, shorthand string, usage string) {
	flag := &pflag.Flag{
		Name:      name,
		Shorthand: shorthand,
		Usage:     usage,
		Value:     &sizePtr{value: value},
	}
	f.AddFlag(flag)
}

type sizePtr struct {
	value **uint64
}

func (d *sizePtr) String() string {
	if d.value == nil {
		return ""
	}
	return fmt.Sprintf("%v", *d.value)
}

func (d *sizePtr) Set(s string) error {
	if s == "" {
		*d.value = nil
		return nil
	}
	v, err := humanize.ParseBytes(s)
	if err != nil {
		return err
	}
	*d.value = &v
	return nil
}

func (d *sizePtr) Type() string {
	return "uint64"
}

func (d *sizePtr) IsBoolFlag() bool {
	return false
}

func OptIntSlice(f *pflag.FlagSet, value **[]int, name, shorthand string, usage string) {
	flag := &pflag.Flag{
		Name:      name,
		Shorthand: shorthand,
		Usage:     usage,
		Value:     &intSlicePtr{value: value},
	}
	f.AddFlag(flag)
}

type intSlicePtr struct {
	value **[]int
}

func (d *intSlicePtr) String() string {
	if d.value == nil || *d.value == nil {
		return ""
	}
	out := make([]string, len(**d.value))
	for i, n := range **d.value {
		out[i] = fmt.Sprintf("%d", n)
	}
	return "[" + strings.Join(out, ",") + "]"
}

func (d *intSlicePtr) Set(s string) error {
	if s == "" {
		*d.value = nil
		return nil
	}

	ss := strings.Split(s, ",")
	out := make([]int, len(ss))
	for i, n := range ss {
		var err error
		out[i], err = strconv.Atoi(n)
		if err != nil {
			return err
		}

	}
	if *d.value == nil {
		*d.value = &out
		return nil
	}
	**d.value = append(**d.value, out...)
	return nil
}

func (d *intSlicePtr) Type() string {
	return "intSlice"
}

func (d *intSlicePtr) IsBoolFlag() bool {
	return false
}
