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
	"bytes"
	"strings"
	"testing"
)

func TestGitConfigParseAndGet(t *testing.T) {
	src := `# top comment
[core]
	repositoryformatversion = 0
	filemode = true
	bare = false
[remote "origin"]
	url = https://example.com/repo
	fetch = +refs/heads/*:refs/remotes/origin/*
	fetch = +refs/tags/*:refs/tags/*
[branch "main"]
	remote = origin
	merge = refs/heads/main
[Section.Sub]
	Key = Value
`
	cfg, err := ParseGitConfig(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	cases := []struct {
		key, want string
	}{
		{"core.repositoryformatversion", "0"},
		{"core.filemode", "true"},
		{"remote.origin.url", "https://example.com/repo"},
		{"branch.main.remote", "origin"},
		{"branch.main.merge", "refs/heads/main"},
		{"section.sub.key", "Value"},
		{"Core.FileMode", "true"},
	}
	for _, c := range cases {
		v, ok, err := cfg.Get(c.key)
		if err != nil || !ok {
			t.Errorf("Get(%q) ok=%v err=%v", c.key, ok, err)
			continue
		}
		if v != c.want {
			t.Errorf("Get(%q) = %q, want %q", c.key, v, c.want)
		}
	}

	all, err := cfg.GetAll("remote.origin.fetch")
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("GetAll returned %d entries, want 2", len(all))
	}
}

func TestGitConfigQuotedValues(t *testing.T) {
	src := `[user]
	name = "Alice Example"
	email = " spaced "
	bio = "line1\nline2\twith\\back"
`
	cfg, err := ParseGitConfig(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := func(k string) string {
		v, _, _ := cfg.Get(k)
		return v
	}
	if got("user.name") != "Alice Example" {
		t.Errorf("name = %q", got("user.name"))
	}
	if got("user.email") != " spaced " {
		t.Errorf("email = %q", got("user.email"))
	}
	if got("user.bio") != "line1\nline2\twith\\back" {
		t.Errorf("bio = %q", got("user.bio"))
	}
}

func TestGitConfigContinuationAndComments(t *testing.T) {
	src := `[core]
	# inline comment skipped
	multiline = first \
second ; trailing
	hashvalue = "value#with#hash"
	bool
`
	cfg, err := ParseGitConfig(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	v, _, _ := cfg.Get("core.multiline")
	if v != "firstsecond" {
		t.Errorf("multiline = %q", v)
	}
	v, _, _ = cfg.Get("core.hashvalue")
	if v != "value#with#hash" {
		t.Errorf("hashvalue = %q", v)
	}
	v, _, _ = cfg.Get("core.bool")
	if v != "true" {
		t.Errorf("bool = %q", v)
	}
}

func TestGitConfigSetAddUnsetWriteRoundtrip(t *testing.T) {
	src := `[core]
	bare = false
[remote "origin"]
	url = https://example.com/repo
	fetch = +refs/heads/*:refs/remotes/origin/*
`
	cfg, err := ParseGitConfig(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.Set("core.bare", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Add("remote.origin.fetch", "+refs/tags/*:refs/tags/*"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Add("branch.dev.remote", "origin"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Add("branch.dev.merge", "refs/heads/dev"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Unset("core.bare"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Add("user.name", "needs # quoting"); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := cfg.Write(&buf); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg2, err := ParseGitConfig(&buf)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if _, ok, _ := cfg2.Get("core.bare"); ok {
		t.Errorf("core.bare should be unset")
	}
	all, _ := cfg2.GetAll("remote.origin.fetch")
	if len(all) != 2 {
		t.Errorf("fetch entries = %d, want 2", len(all))
	}
	if v, _, _ := cfg2.Get("branch.dev.merge"); v != "refs/heads/dev" {
		t.Errorf("branch.dev.merge = %q", v)
	}
	if v, _, _ := cfg2.Get("user.name"); v != "needs # quoting" {
		t.Errorf("user.name = %q", v)
	}
}

func TestGitConfigInvalidKey(t *testing.T) {
	cfg := &GitConfig{}
	if err := cfg.Add("nodot", "x"); err == nil {
		t.Errorf("expected error for keyless add")
	}
	if _, err := cfg.GetAll("nodot"); err == nil {
		t.Errorf("expected error for keyless get")
	}
}
