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
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strings"

	"golang.org/x/term"

	"github.com/google/abfs/manifest"
)

func main() {
	groups := os.Args[1:]

	if len(groups) == 0 {
		fmt.Printf("Usage: %s \"<regex>\" [\"<more-regex>\"]...\n", os.Args[0])
		os.Exit(1)
	}

	groupsRegex := regexp.MustCompile(strings.Join(groups, "|"))

	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "expected to read the manifest XML from stdin")
	}

	manBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		log.Fatalf("cannot read stdin: %v", err)
	}

	man, err := manifest.Parse(manBytes)
	if err != nil {
		log.Fatalf("cannot parse manifest file read from stdin: %v", err)
	}

	newProjects := make([]*manifest.Project, 0, len(man.Project))
	for _, prj := range man.Project {
		if prj.GroupsMatch(groupsRegex) {
			newProjects = append(newProjects, prj)
		}
	}

	man.Project = newProjects
	buf, err := xml.MarshalIndent(man, "", " ")
	if err != nil {
		log.Fatalf("cannot serialize manifest: %v", err)
	}

	if _, err := os.Stdout.Write(buf); err != nil {
		log.Fatalf("cannot write to stdout: %v", err)
	}
}
