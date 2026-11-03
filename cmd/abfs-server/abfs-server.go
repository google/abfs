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

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/pflag"

	"github.com/google/abfs/cmd/abfs/server"
	"github.com/google/abfs/shared/cmd/root"
)

func main() {
	fl := pflag.NewFlagSet(os.Args[0], pflag.ExitOnError)
	fl.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Description: Start ABFS server for blobs, objects and map results.\n")
		fl.PrintDefaults()
		os.Exit(1)
	}

	cmd := server.NewStandaloneGCPServerCmd(root.NewRootCommand())
	cmd.RegisterFlags(fl)

	if err := fl.Parse(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse flags: %v\n", err)
		os.Exit(1)
	}

	if err := cmd.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "server failed: %v\n", err)
		os.Exit(1)
	}
}
