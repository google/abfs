// Copyright 2025 Google LLC
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
	"log"
	"os"
)

// Simple test executable that will create a new file in read only mode
func main() {
	args := os.Args
	argCount := len(args)
	if argCount != 2 && argCount != 3 {
		log.Fatalf("usage: %s filename [exec]", args[0])
	}
	filename := args[1]
	var perm os.FileMode
	perm = os.FileMode(0444)
	if argCount > 2 {
		perm = os.FileMode(0555)
	}
	file, err := os.OpenFile(filename, os.O_CREATE, perm)
	if err != nil {
		log.Fatalf("Failed to create file: %s %v", filename, err)
	}
	file.Close()
}
