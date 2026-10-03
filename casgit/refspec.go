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

package casgit

import (
	"fmt"
	"strings"
)

type RefSpec struct {
	Source      string
	Destination string
	IsNegative  bool
	IsWildcard  bool
}

func ParseRefSpec(spec string) (RefSpec, error) {
	if strings.HasPrefix(spec, "^") {
		return RefSpec{Source: spec[1:], IsNegative: true}, nil
	}
	spec = strings.TrimPrefix(spec, "+")
	parts := strings.Split(spec, ":")
	if len(parts) == 1 {
		return RefSpec{Source: parts[0]}, nil
	} else if len(parts) == 2 {
		return RefSpec{Source: parts[0], Destination: parts[1], IsWildcard: strings.Contains(parts[0], "*")}, nil
	}
	return RefSpec{}, fmt.Errorf("invalid refspec: %q", spec)
}

func ParseRefSpecs(specs []string) ([]RefSpec, error) {
	parsed := make([]RefSpec, 0, len(specs))
	for _, spec := range specs {
		r, err := ParseRefSpec(spec)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, r)
	}
	return parsed, nil
}

func (r RefSpec) Match(ref string) bool {
	if r.IsWildcard {
		prefix := strings.TrimSuffix(r.Source, "*")
		return strings.HasPrefix(ref, prefix)
	}
	return ref == r.Source
}

func (r RefSpec) Map(ref string) string {
	if r.IsWildcard {
		prefix := strings.TrimSuffix(r.Source, "*")
		suffix := strings.TrimPrefix(ref, prefix)
		destPrefix := strings.TrimSuffix(r.Destination, "*")
		return destPrefix + suffix
	}
	return r.Destination
}

// RefSpecsMatch determines if the given reference matches the provided list of RefSpecs.
// A reference is considered matching if:
//  1. It does not match any negative RefSpec.
//  2. Either no positive specs are specified or it matches at least one positive RefSpec.
func RefSpecsMatch(ref string, specs []RefSpec) (string, bool) {
	hasPositive := false
	for _, spec := range specs {
		if !spec.IsNegative {
			hasPositive = true
		} else if spec.Match(ref) {
			return "", false
		}
	}

	if !hasPositive {
		return ref, true
	}

	for _, spec := range specs {
		if !spec.IsNegative && spec.Match(ref) {
			return spec.Map(ref), true
		}
	}

	return "", false
}

func commonPrefix(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	prefix := strs[0]
	for _, s := range strs[1:] {
		n := min(len(s), len(prefix))
		i := 0
		for i < n && prefix[i] == s[i] {
			i++
		}
		prefix = prefix[:i]
		if prefix == "" {
			break
		}
	}
	return prefix
}

func RefSpecsPrefix(specs []RefSpec) string {
	strs := make([]string, 0, len(specs))
	for _, spec := range specs {
		if spec.IsNegative {
			continue
		}
		s := strings.TrimSuffix(spec.Source, "*")
		if !strings.HasSuffix(s, "/") {
			s += "/"
		}
		strs = append(strs, s)
	}
	r := commonPrefix(strs)
	return strings.Trim(r, "/")
}
