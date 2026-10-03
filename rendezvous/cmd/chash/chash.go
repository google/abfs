// Copyright 2023 Google LLC
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
	"strings"

	"github.com/google/abfs/rendezvous"
	"github.com/spf13/pflag"
)

func main() {
	var n, index int
	pflag.IntVarP(&n, "size", "n", 0, "Total number of workers")
	pflag.IntVarP(&index, "index", "i", -1, "Index of current worker")
	pflag.Parse()

	if n == 0 {
		log.Fatal("size must be specified")
	}
	if index == -1 {
		log.Fatal("index of the node must be specified")
	}
	if index < 0 || index >= n {
		log.Fatalf("provided index (%d) is out of range for provided workers (%d)", index, n)
	}

	scanner := bufio.NewScanner(os.Stdin)
	chasher := rendezvous.NewChasher(n, index)
	for scanner.Scan() {
		key := strings.TrimSpace(scanner.Text())
		if key == "" {
			continue
		}
		if chasher.IsAssignedToCurrentWorker(key) {
			fmt.Println(key)
		}
	}
	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}
}
