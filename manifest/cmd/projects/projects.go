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
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/google/abfs/manifest"
)

func main() {
	var groups []string
	var path, name bool
	pflag.BoolVarP(&path, "path", "p", false, "Print the project paths")
	pflag.BoolVarP(&name, "name", "n", false, "Print the project names")
	pflag.StringArrayVarP(&groups, "group", "g", nil, "Filter projects based on the group using regex")
	pflag.Parse()

	var groupsRegex *regexp.Regexp
	if len(groups) != 0 {
		groupsRegex = regexp.MustCompile(strings.Join(groups, "|"))
	}

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

	for _, prj := range man.Project {
		if groupsRegex != nil && !prj.GroupsMatch(groupsRegex) {
			continue
		}
		if path && name {
			fmt.Println(prj.Name, prj.GetPath())
		} else if path { // name == false
			fmt.Println(prj.GetPath())
		} else { // path == false
			// default to printing the project names even if --name is not specified
			fmt.Println(prj.Name)
		}
	}
}
