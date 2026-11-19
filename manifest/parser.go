// Copyright 2016 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package manifest

import (
	"encoding/xml"
	"os"
	"slices"
	"strings"
	"unicode"
)

func (p *Project) parse() {
	p.Groups = parseGroups(p.GroupsString)
}

func (e *ExtendProject) parse() {
	e.Groups = parseGroups(e.GroupsString)
}

// addGroups returns the union of both group lists, sorted as repo writes them.
func addGroups(groups, extra []string) []string {
	if len(extra) == 0 {
		return groups
	}
	union := slices.Clone(groups)
	for _, g := range extra {
		if !slices.Contains(union, g) {
			union = append(union, g)
		}
	}
	slices.Sort(union)
	return union
}

// parseGroups splits a groups attribute as repo does: on commas and white space,
// dropping empty and repeated names.
func parseGroups(s string) []string {
	var groups []string
	for _, g := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		if !slices.Contains(groups, g) {
			groups = append(groups, g)
		}
	}
	return groups
}

// Parse parses the given XML data.
func Parse(contents []byte) (*Manifest, error) {
	var m Manifest
	if err := xml.Unmarshal(contents, &m); err != nil {
		return nil, err
	}

	for i := range m.Project {
		m.Project[i].parse()
	}
	for i := range m.ExtendProject {
		m.ExtendProject[i].parse()
	}
	return &m, nil
}

// ParseFile reads and parses an XML file
func ParseFile(name string) (*Manifest, error) {
	content, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return Parse(content)
}
