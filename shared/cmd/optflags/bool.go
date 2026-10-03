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

	"github.com/spf13/pflag"
)

func OptBool(f *pflag.FlagSet, value **bool, name, shorthand string, usage string) {
	flag := &pflag.Flag{
		Name:        name,
		Shorthand:   shorthand,
		Usage:       usage,
		Value:       &boolPtr{value: value},
		NoOptDefVal: "true",
	}
	f.AddFlag(flag)
}

type boolPtr struct {
	value **bool
}

func (b *boolPtr) String() string {
	if b.value == nil {
		return ""
	}
	return fmt.Sprintf("%v", *b.value)
}

func (b *boolPtr) Set(s string) error {
	if s == "" {
		*b.value = nil
		return nil
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	*b.value = &v
	return nil
}

func (b *boolPtr) Type() string {
	return "bool"
}

func (b *boolPtr) IsBoolFlag() bool {
	return true
}
