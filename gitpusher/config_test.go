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

package gitpusher

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestValidateGitServerAndProject(t *testing.T) {
	tests := []struct {
		name        string
		gitURL      string
		server      string
		project     string
		wantGitURL  string
		wantServer  string
		wantProject string
		wantErr     bool
	}{
		{
			name:        "valid https url empty server project",
			gitURL:      "https://github.com/foo/bar",
			wantGitURL:  "https://github.com/foo/bar",
			wantServer:  "github.com",
			wantProject: "foo/bar",
		},
		{
			name:        "valid ssh url empty server project",
			gitURL:      "ssh://git@github.com/foo/bar",
			wantGitURL:  "ssh://git@github.com/foo/bar",
			wantServer:  "github.com",
			wantProject: "foo/bar",
		},
		{
			name:        "url no scheme defaults to https",
			gitURL:      "github.com/foo/bar",
			wantGitURL:  "https://github.com/foo/bar",
			wantServer:  "github.com",
			wantProject: "foo/bar",
		},
		{
			name:        "url double slash defaults to https",
			gitURL:      "//github.com/foo/bar",
			wantGitURL:  "https://github.com/foo/bar",
			wantServer:  "github.com",
			wantProject: "foo/bar",
		},
		{
			name:        "empty url populated from server and project",
			server:      "gitlab.com",
			project:     "group/project",
			wantGitURL:  "https://gitlab.com/group/project",
			wantServer:  "gitlab.com",
			wantProject: "group/project",
		},
		{
			name:        "empty url populated from server and project with clean",
			server:      "gitlab.com/",
			project:     "/group/project/",
			wantGitURL:  "https://gitlab.com/group/project",
			wantServer:  "gitlab.com",
			wantProject: "group/project",
		},
		{
			name:        "url provided server and project preserved if set",
			gitURL:      "https://github.com/foo/bar",
			server:      "otherserver.com",
			project:     "other/project",
			wantGitURL:  "https://github.com/foo/bar",
			wantServer:  "otherserver.com",
			wantProject: "other/project",
		},
		{
			name:    "empty everything error",
			wantErr: true,
		},
		{
			name:    "empty url missing project error",
			server:  "github.com",
			wantErr: true,
		},
		{
			name:    "empty url missing server error",
			project: "foo/bar",
			wantErr: true,
		},
		{
			name:    "url missing host error",
			gitURL:  "/foo/bar",
			wantErr: true,
		},
		{
			name:    "url missing project error",
			gitURL:  "https://github.com",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, server, project := tt.gitURL, tt.server, tt.project
			err := validateGitServerAndProject(&url, &server, &project)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateGitServerAndProject() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if url != tt.wantGitURL {
					t.Errorf("validateGitServerAndProject() url = %v, want %v", url, tt.wantGitURL)
				}
				if server != tt.wantServer {
					t.Errorf("validateGitServerAndProject() server = %v, want %v", server, tt.wantServer)
				}
				if project != tt.wantProject {
					t.Errorf("validateGitServerAndProject() project = %v, want %v", project, tt.wantProject)
				}
			}
		})
	}
}

// Ensure the helper works as expected too, indirectly tested above but good to verify specific edge cases if needed.
func TestParseURLWithDefaultScheme(t *testing.T) {
	tests := []struct {
		url     string
		scheme  string
		wantURL string
		wantErr bool
	}{
		{"example.com/foo", "https", "https://example.com/foo", false},
		{"//example.com/foo", "http", "http://example.com/foo", false},
		{"ssh://git@example.com/foo", "https", "ssh://git@example.com/foo", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			u, err := parseURLWithDefaultScheme(tt.url, tt.scheme)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseURLWithDefaultScheme() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && u.String() != tt.wantURL {
				t.Errorf("parseURLWithDefaultScheme() = %v, want %v", u.String(), tt.wantURL)
			}
		})
	}
}

func TestConfigIndex(t *testing.T) {
	rawConfig := `
pushers-count: 3
pushers:
- name: pusher-0
- name: pusher-1
- name: pusher-2`

	var c GitPusherPoolConfig
	err := yaml.Unmarshal([]byte(rawConfig), &c)
	require.NoError(t, err)

	require.NoError(t, c.Validate())

	require.Equal(t, 3, c.PushersCount)
	require.Equal(t, 3, len(c.Pushers))
	require.Equal(t, 0, *c.Pushers[0].Index)
	require.Equal(t, 1, *c.Pushers[1].Index)
	require.Equal(t, 2, *c.Pushers[2].Index)
}

func TestConfigMaxMemoryUsage(t *testing.T) {
	rawConfig := `
pushers-count: 3
defaults:
  max-memory-usage: 80
pushers:
- name: pusher-0
- name: pusher-1
  max-memory-usage: 85
- name: pusher-2`

	var c GitPusherPoolConfig
	err := yaml.Unmarshal([]byte(rawConfig), &c)
	require.NoError(t, err)

	require.NoError(t, c.Validate())

	require.Equal(t, float64(80.0), c.Defaults.MaxMemoryUsage)
	require.Equal(t, float64(80.0), c.Pushers[0].MaxMemoryUsage)
	require.Equal(t, float64(85.0), c.Pushers[1].MaxMemoryUsage)
	require.Equal(t, float64(80.0), c.Pushers[2].MaxMemoryUsage)
}

func TestConfigMaxMemoryUsageInvalid(t *testing.T) {
	tests := []struct {
		name      string
		rawConfig string
	}{
		{
			name: "negative max memory usage",
			rawConfig: `
pushers-count: 1
defaults:
  max-memory-usage: -10
pushers:
- name: pusher-0`,
		},
		{
			name: "max memory usage too high",
			rawConfig: `
pushers-count: 1
defaults:
  max-memory-usage: 110
pushers:
- name: pusher-0`,
		},
		{
			name: "pusher negative max memory usage",
			rawConfig: `
pushers-count: 1
pushers:
- name: pusher-0
  max-memory-usage: -1`,
		},
		{
			name: "pusher max memory usage too high",
			rawConfig: `
pushers-count: 1
pushers:
- name: pusher-0
  max-memory-usage: 101`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c GitPusherPoolConfig
			err := yaml.Unmarshal([]byte(tt.rawConfig), &c)
			require.NoError(t, err)

			err = c.Validate()
			require.Error(t, err)
		})
	}
}
