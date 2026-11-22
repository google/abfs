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

package repo

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

func TestSetManifest_RemovesAndExtends(t *testing.T) {
	manifestXML := `
<manifest>
  <remote name="aosp" fetch="https://android.googlesource.com/" />
  <default revision="main" remote="aosp" />
  <project name="platform/keep" path="keep" />
  <project name="platform/gone" path="gone" revision="not-pushed" />
  <remove-project name="platform/gone" />
  <project name="platform/twice" path="twice/a" />
  <project name="platform/twice" path="twice/b" />
  <remove-project name="platform/twice" path="twice/b" optional="true" />
  <project name="platform/ext" path="ext" />
  <extend-project name="platform/ext" revision="release" groups="extra" dest-path="moved" />
</manifest>
`
	mainHash := entities.NewHash("1234567890abcdef1234567890abcdef12345678")
	releaseHash := entities.NewHash("fedcba0987654321fedcba0987654321fedcba09")
	store := &testStore{}
	store.searchRefsFunc = func(server, project, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
		switch refPrefix {
		case "main", "refs/heads/main":
			return []entities.ProjectRefHash{
				{Project: "platform/keep", Ref: refPrefix, Hash: mainHash},
				{Project: "platform/twice", Ref: refPrefix, Hash: mainHash},
				{Project: "platform/ext", Ref: refPrefix, Hash: mainHash},
			}, nil
		case "release", "refs/heads/release":
			return []entities.ProjectRefHash{{Project: "platform/ext", Ref: refPrefix, Hash: releaseHash}}, nil
		}
		return nil, nil
	}
	r := New(store, &testCASGit{}, "somehost.googlesource.com", "manifest", "https", "", false)
	require.NoError(t, r.ManifestFromBytes([]byte(manifestXML)))

	type project struct {
		Name, Path, Revision string
		Groups               []string
	}
	var got []project
	for _, p := range r.PinnedManifest().Project {
		got = append(got, project{p.Name, p.GetPath(), p.Revision, p.Groups})
	}
	require.Equal(t, []project{
		{"platform/keep", "keep", mainHash.String(), nil},
		{"platform/twice", "twice/a", mainHash.String(), nil},
		{"platform/ext", "moved", releaseHash.String(), []string{"extra"}},
	}, got)
	require.Len(t, r.Manifest().RemoveProject, 2)
	require.Len(t, r.Manifest().ExtendProject, 1)
}

func TestSetManifest_BaseRevMismatch(t *testing.T) {
	manifestXML := `
<manifest>
  <remote name="aosp" fetch="https://android.googlesource.com/" />
  <default revision="main" remote="aosp" />
  <project name="platform/ext" path="ext" />
  <extend-project name="platform/ext" revision="release" base-rev="old" />
</manifest>
`
	store := &testStore{}
	store.searchRefsFunc = func(server, project, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
		return nil, nil
	}
	r := New(store, &testCASGit{}, "somehost.googlesource.com", "manifest", "https", "", false)
	require.EqualError(t, r.ManifestFromBytes([]byte(manifestXML)),
		"detected base-revision mismatch, updates needed:\nextend-project name platform/ext:\n  base old vs revision main")
}

func TestSetManifest_WarningsNameTheFile(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	file := filepath.Join(t.TempDir(), "layered.xml")
	require.NoError(t, os.WriteFile(file, []byte(`<manifest>
  <remote name="aosp" fetch="https://android.googlesource.com/" />
  <default revision="main" remote="aosp" />
  <project name="platform/keep" path="keep" rebase="false" />
</manifest>
`), 0644))
	store := &testStore{}
	store.searchRefsFunc = func(server, project, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
		return []entities.ProjectRefHash{{Project: "platform/keep", Ref: refPrefix, Hash: entities.NewHash("1234567890abcdef1234567890abcdef12345678")}}, nil
	}
	r := New(store, &testCASGit{}, "somehost.googlesource.com", "manifest", "https", "", false)
	require.NoError(t, r.ManifestFromFile(file))
	require.Contains(t, logs.String(), "Warning: manifest "+file+`: <project name="platform/keep">: attribute "rebase" is not applied`)
}
