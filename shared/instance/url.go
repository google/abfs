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

package instance

import (
	"net/url"
	"path"
	"strings"
)

func Scheme(in string) string {
	parsed, err := url.Parse(in)
	if err != nil {
		return ""
	}

	return parsed.Scheme
}

func MaybeRemoveScheme(in string) string {
	if in == "" {
		return in
	}

	// The parsing library can get tripped up by something like:
	// something.googlesource.com:2914
	// It thinks something.googlesource.com is the scheme
	// In this case, we need to insert something to make it scheme relative
	if !strings.Contains(in, "://") {
		in = "//" + in
	}

	parsed, err := url.Parse(in)
	if err != nil {
		return in
	}
	parsed.Scheme = ""

	return path.Clean(strings.TrimPrefix(parsed.String(), "//"))
}

// Adds a scheme to input URL if one does not exist.
func MaybeAddScheme(in, scheme string) string {
	// The parsing library can get tripped up by something like:
	// something.googlesource.com:2914
	// It thinks something.googlesource.com is the scheme
	// In this case, we need to insert something to make it scheme relative
	if !strings.Contains(in, "://") {
		in = "//" + in
	}

	parsed, err := url.Parse(in)
	if err != nil {
		return in
	}
	if parsed.Scheme == "" {
		parsed.Scheme = scheme
	}

	return parsed.String()
}
