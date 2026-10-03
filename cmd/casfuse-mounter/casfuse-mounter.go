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
	"bufio"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/spf13/pflag"
	"golang.org/x/sys/unix"

	"github.com/google/abfs/shared/version"
)

var (
	maxNrOpFile string
	showVersion bool
)

func readMaxOpenFiles(maxFileName string) (maxValue int, retErr error) {
	// Read
	// Need to use scan as direct fscanf only returns a single char
	f, err := os.Open(maxFileName)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := f.Close(); err != nil {
			retErr = err
		}
	}()

	scanner := bufio.NewScanner(f)
	scanner.Scan()
	maxValue, err = strconv.Atoi(scanner.Text())

	if err != nil {
		return 0, err
	}
	return maxValue, nil
}

func main() {
	pflag.StringVarP(&maxNrOpFile, "max-nr-open-file", "f", "/proc/sys/fs/nr_open", "File containing maximum value for number of open files for a process.")
	pflag.BoolVar(&showVersion, "version", false, "Print the application version")

	pflag.Parse()

	if showVersion {
		fmt.Println(version.Version())
		return
	}

	args := pflag.Args()
	if len(args) != 1 {
		log.Fatalf("Expected a single PID!\nUsage: %s [-f <max file open file>] <PID> ", os.Args[0])
	}

	pid, err := strconv.Atoi(args[0])
	if err != nil {
		log.Fatal(err)
	}
	maxValue, err := readMaxOpenFiles(maxNrOpFile)
	if err != nil {
		log.Fatal(err)
	}

	var rlim unix.Rlimit
	rlim.Max = uint64(maxValue)
	rlim.Cur = uint64(maxValue)
	err = unix.Prlimit(pid, unix.RLIMIT_NOFILE, &rlim, nil)
	if err != nil {
		log.Fatal(err)
	}
}
