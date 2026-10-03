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
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"

	"github.com/google/abfs/binding/tap"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/version"

	flag "github.com/spf13/pflag"
)

func main() {
	tapPathPtr := flag.String("tapPath", "", "Path to tap")
	prefixPathPtr := flag.String("prefix", "", "Prefix relative to mount")
	prettyPtr := flag.Bool("pretty", false, "Pretty print JSON")
	showRawPtr := flag.Bool("raw", false, "Show the raw output captured from the file-system")
	strictPtr := flag.Bool("strict", false, "Run in strict mode, fail command if dirty read files are found")
	stdinPtr := flag.Bool("stdin", false, "Pass the tap stdin to the underlying process")
	outDirPtr := flag.String("outdir", entities.OutDir, "The path to the output directory")
	showVersion := flag.Bool("version", false, "Print the application version")
	snapshotConcurrencyPtr := flag.Int("snapshot-concurrency", 1, "Number of concurrent threads to use for snapshotting. Set <= 0 for max (NumCPU).")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Version())
		return
	}

	if len(flag.Args()) == 0 {
		log.Fatalf("No command provided")
	}
	cmd := exec.Command(flag.Args()[0], flag.Args()[1:]...)
	if *stdinPtr {
		cmd.Stdin = os.Stdin
	}

	// Run input command
	tapResult, err := tap.RunTapCmd(cmd, *tapPathPtr, *showRawPtr, *prefixPathPtr, *strictPtr, *outDirPtr, *snapshotConcurrencyPtr)
	if err != nil {
		log.Fatalf("Failed to run tap cmd: %s\n", err)
	}

	enc := json.NewEncoder(os.Stdout)
	if *prettyPtr {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(tapResult); err != nil {
		log.Fatalf("JSON failure %s\n", err)
	}
}
