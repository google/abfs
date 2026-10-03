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
	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd/clierr"
)

const usage = "Run the command with --help for the full usage information."

func EnsureNoArgs(fl *pflag.FlagSet) {
	if fl.NArg() != 0 {
		panic(clierr.CLIErr("Expected no arguments. %s", usage))
	}
}

func EnsureArgs(fl *pflag.FlagSet) []string {
	if fl.NArg() == 0 {
		panic(clierr.CLIErr("No arguments specified. %s", usage))
	}
	return fl.Args()
}

func EnsureSingleArg(fl *pflag.FlagSet) string {
	if fl.NArg() != 1 {
		panic(clierr.CLIErr("Expected single argument. %s", usage))
	}
	return fl.Arg(0)
}

func SingleArgOrDefault(fl *pflag.FlagSet, def string) string {
	switch fl.NArg() {
	case 0:
		return def
	case 1:
		return fl.Arg(0)
	default:
		panic(clierr.CLIErr("Too many arguments. %s", usage))
	}
}

func EnsureTwoArgs(fl *pflag.FlagSet) (string, string) {
	if fl.NArg() != 2 {
		panic(clierr.CLIErr("Expected two arguments. %s", usage))
	}
	return fl.Arg(0), fl.Arg(1)
}
