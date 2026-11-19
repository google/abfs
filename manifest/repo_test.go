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

package manifest

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// readArchive splits a txtar archive into its files.
func readArchive(t *testing.T, name string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(name)
	require.NoError(t, err)
	files := make(map[string]string)
	var current string
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if strings.HasPrefix(line, "-- ") && strings.HasSuffix(line, " --\n") {
			current = strings.TrimSuffix(strings.TrimPrefix(line, "-- "), " --\n")
			continue
		}
		if current != "" {
			files[current] += line
		}
	}
	return files
}

// repoTuples describes each project as the archives in testdata/repo do.
func repoTuples(m *Manifest) []string {
	var tuples []string
	for _, p := range m.Project {
		remote := p.Remote
		if remote == "" {
			remote = m.Default.Remote
		}
		upstream := p.Upstream
		if upstream == "" {
			upstream = m.Default.Upstream
		}
		destBranch := p.DestBranch
		if destBranch == "" {
			destBranch = m.Default.DestBranch
		}
		groups := slices.Sorted(slices.Values(p.Groups))
		tuples = append(tuples, strings.Join([]string{p.GetPath(), p.Name, m.effectiveRevision(p), remote, strings.Join(groups, ","), upstream, destBranch}, "\t"))
	}
	slices.Sort(tuples)
	return tuples
}

// The archives in testdata/repo hold generated manifests and the projects the repo tool
// gives for them.
func TestEffective_MatchesRepo(t *testing.T) {
	archives, err := filepath.Glob("testdata/repo/*.txtar")
	require.NoError(t, err)
	require.NotEmpty(t, archives)
	for _, archive := range archives {
		t.Run(filepath.Base(archive), func(t *testing.T) {
			files := readArchive(t, archive)
			bytes := func(ctx context.Context, name string) ([]byte, error) {
				return []byte(files[name]), nil
			}
			m, err := Parse([]byte(files["default.xml"]))
			require.NoError(t, err)
			require.NoError(t, m.FlattenIncludes(context.Background(), bytes))
			err = m.Effective()
			if want, ok := files["want-error"]; ok {
				require.ErrorContains(t, err, strings.TrimSpace(want))
				return
			}
			require.NoError(t, err)
			require.Equal(t, strings.Split(strings.TrimRight(files["want"], "\n"), "\n"), repoTuples(m))
		})
	}
}
