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

package ninjago

import (
	"fmt"
	"os"
	"strings"
)

var envVarIgnoreList = map[string]struct{}{
	"HOME":     {},
	"USER":     {},
	"HOSTNAME": {},
	"TOP":      {},
	// This env var was pointing to a randomly generated temp directory that was different every build
	// e.g. "CIPD_PROXY_URL": "unix://CWD/out/soong/.temp/cipd1018238983/proxy.unix". See https://chromium.googlesource.com/infra/luci/luci-go/+/HEAD/cipd/
	// for context on what CIPD is.
	"CIPD_PROXY_URL": {},
}

// NormalizedEnvVars returns a map of environment variables that have been normalized
// using the default ignore list.
func NormalizedEnvVars() map[string]string {
	osVars := os.Environ()
	cwd, _ := os.Getwd()
	return NormalizeEnvVars(osVars, cwd, func(key string) bool {
		_, ignore := envVarIgnoreList[key]
		return !ignore
	})
}

// NormalizeEnvVars returns a map of environment variables that have been normalized
// to have "CWD" instead of the current working directory. The prefix "CWD/" is removed
// from the values to help match environments with absolute vs. relative paths (unless
// the value contains a path separator, in which case it is left unchanged).
// The keep function is used to filter environment variables. If keep returns true, the
// variable is included in the result.
func NormalizeEnvVars(osVars []string, cwd string, keep func(string) bool) map[string]string {
	result := make(map[string]string, len(osVars))
	replacer := strings.NewReplacer(cwd, "CWD")
	for _, kv := range osVars {
		ss := strings.SplitN(kv, "=", 2)
		if len(ss) < 2 {
			continue
		}
		key, value := ss[0], ss[1]
		if !keep(key) {
			continue
		}
		value = replacer.Replace(value)
		// We are not trimming lists of paths, because there are cases like $PATH where it is required
		// to be absolute paths and ARES rebuilds the environment based on these values.
		// There were errors like this:
		// `exec: "bash": cannot run executable found relative to current directory`
		if !strings.Contains(value, string(os.PathListSeparator)) {
			value = strings.TrimPrefix(value, "CWD/")
		}
		result[key] = value
	}
	return result
}

// DenormalizeEnvVars expands the env vars by replacing CWD with the current working directory.
func DenormalizeEnvVars(envVars map[string]string, cwd string) []string {
	replacer := strings.NewReplacer("CWD", cwd)
	envvars := make([]string, 0, len(envVars))
	for k, v := range envVars {
		v = replacer.Replace(v)
		envvars = append(envvars, fmt.Sprintf("%s=%s", k, v))
	}
	return envvars
}
