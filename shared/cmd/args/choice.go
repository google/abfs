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

package args

import (
	"slices"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd/clierr"
)

func OptChoice(f *pflag.FlagSet, value *string, name, shorthand string, defaultValue string, choices []string, usage string) {
	*value = defaultValue

	flag := &pflag.Flag{
		Name:      name,
		Shorthand: shorthand,
		Usage:     usage,
		DefValue:  defaultValue,
		Value: &choiceValue{
			value:        value,
			choices:      choices,
			defaultValue: defaultValue,
		},
	}

	f.AddFlag(flag)
}

type choiceValue struct {
	value        *string
	choices      []string
	defaultValue string
}

func (d *choiceValue) String() string {
	if *d.value == "" {
		return d.defaultValue
	}
	return *d.value
}

func (d *choiceValue) Set(s string) error {
	if s == "" {
		*d.value = d.defaultValue
		return nil
	}
	if !slices.Contains(d.choices, s) {
		return clierr.CLIErr("must be one of %v", d.choices)
	}
	*d.value = s
	return nil
}

func (d *choiceValue) Type() string {
	return "string"
}

func (d *choiceValue) IsBoolFlag() bool {
	return false
}

func (d *choiceValue) Choices() []string {
	return d.choices
}
