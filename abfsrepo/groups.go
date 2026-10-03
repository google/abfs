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

package abfsrepo

import "strings"

const (
	groupsAll       = "all"
	groupDefault    = "default"
	groupNotDefault = "notdefault"
)

var defaultGroups = []string{groupDefault}

func GroupsMatch(prjGroups []string, filterInclude, filterExclude map[string]struct{}) bool {
	if len(prjGroups) == 0 {
		prjGroups = defaultGroups
	}

	if groupsMatch(prjGroups, filterExclude) {
		return false
	}
	return groupsMatch(prjGroups, filterInclude)
}

func groupsMatch(prjGroups []string, filter map[string]struct{}) bool {
	if _, ok := filter[groupsAll]; ok {
		return true
	}
	hasNotDefault := false
	for _, g := range prjGroups {
		if _, ok := filter[g]; ok {
			return true
		}
		if g == groupNotDefault {
			hasNotDefault = true
		}
	}
	if _, ok := filter[groupDefault]; ok {
		return !hasNotDefault
	}
	return false
}

func FilterGroups(groups []string) (map[string]struct{}, map[string]struct{}) {
	if len(groups) == 0 {
		groups = defaultGroups
	}
	include := make(map[string]struct{})
	exclude := make(map[string]struct{})
	for _, g := range groups {
		if trimmed, ok := strings.CutPrefix(g, "-"); ok {
			exclude[trimmed] = struct{}{}
		} else {
			include[g] = struct{}{}
		}
	}
	return include, exclude
}
