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
	"strconv"

	"github.com/spf13/pflag"
)

func OptFloat64(f *pflag.FlagSet, value **float64, name, shorthand string, defValue float64, usage string) {
	// Initialize with the default value so it matches standard pflag behavior
	if *value == nil {
		*value = &defValue
	}
	flag := &pflag.Flag{
		Name:      name,
		Shorthand: shorthand,
		Usage:     usage,
		Value:     &float64Ptr{value: value},
		DefValue:  strconv.FormatFloat(defValue, 'g', -1, 64),
	}
	f.AddFlag(flag)
}

type float64Ptr struct {
	value **float64
}

func (d *float64Ptr) String() string {
	if d.value == nil || *d.value == nil {
		return ""
	}
	return strconv.FormatFloat(**d.value, 'g', -1, 64)
}

func (d *float64Ptr) Set(s string) error {
	if s == "" {
		*d.value = nil
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*d.value = &v
	return nil
}

func (d *float64Ptr) Type() string {
	return "float64"
}

func (d *float64Ptr) IsBoolFlag() bool {
	return false
}
