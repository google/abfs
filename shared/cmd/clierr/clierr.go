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

package clierr

import (
	"fmt"
	"os"

	"github.com/spf13/pflag"
)

type CLIError struct {
	message  string
	exitCode int
}

func (e *CLIError) Error() string {
	return e.message
}

func (e *CLIError) ExitCode() int {
	return e.exitCode
}

func CLIErrNoMessage(exitCode int) *CLIError {
	return &CLIError{message: "", exitCode: exitCode}
}

func CLIErr(format string, args ...any) *CLIError {
	return CLIErrWithExitCode(1, format, args...)
}

func CLIErrWithExitCode(exitCode int, format string, args ...any) *CLIError {
	return &CLIError{message: fmt.Sprintf(format, args...), exitCode: exitCode}
}

func CLIUsage(fl *pflag.FlagSet, format string, args ...any) *CLIError {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	fl.Usage()
	return CLIErrNoMessage(1)
}

func NoSubcommandErr(fl *pflag.FlagSet) *CLIError {
	fl.Usage()

	fmt.Fprintln(os.Stderr, "")
	return CLIErr("No subcommands specified.")
}
