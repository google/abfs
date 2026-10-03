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

	"github.com/spf13/pflag"
)

func OptVar[T any, PT interface {
	*T
	pflag.Value
}](f *pflag.FlagSet, value **T, name, shorthand string, usage string) {
	flag := &pflag.Flag{
		Name:      name,
		Shorthand: shorthand,
		Usage:     usage,
		Value:     &varPtr[T, PT]{value: value},
	}
	f.AddFlag(flag)
}

type varPtr[T any, PT interface {
	*T
	pflag.Value
}] struct {
	value **T
}

func (b *varPtr[T, PT]) String() string {
	if b.value == nil {
		return ""
	}
	return fmt.Sprintf("%v", *b.value)
}

func (b *varPtr[T, PT]) Set(s string) error {
	if s == "" {
		*b.value = nil
		return nil
	}
	var v PT = new(T)
	*b.value = v
	return v.Set(s)
}

func (b *varPtr[T, PT]) Type() string {
	return fmt.Sprintf("%T", *b.value)
}

func (b *varPtr[T, PT]) IsBoolFlag() bool {
	return false
}
