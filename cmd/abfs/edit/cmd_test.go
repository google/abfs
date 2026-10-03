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

package edit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/abfs/shared/hashpath"
)

func TestFileExtensionAndIsYAML(t *testing.T) {
	tests := []struct {
		raw      string
		wantExt  string
		wantYAML bool
	}{
		{raw: "config:server/ares", wantExt: ".yaml", wantYAML: true},
		{raw: "config:clients/configs", wantExt: ".yaml", wantYAML: true},
		{raw: "config:custom.json", wantExt: ".json", wantYAML: true},
		{raw: "branch:path/to/file.yaml", wantExt: ".yaml", wantYAML: true},
		{raw: "branch:path/to/file.txt", wantExt: ".txt", wantYAML: false},
		{raw: "branch:path/to/noext", wantExt: ".tmp", wantYAML: false},
	}

	for _, tc := range tests {
		hp, err := hashpath.Parse(tc.raw)
		if err != nil {
			t.Fatalf("Parse(%q) failed: %v", tc.raw, err)
		}
		c := &editCmd{hashPath: hp}
		if got := c.fileExtension(); got != tc.wantExt {
			t.Errorf("fileExtension(%q) = %q, want %q", tc.raw, got, tc.wantExt)
		}
		if got := c.isYAML(); got != tc.wantYAML {
			t.Errorf("isYAML(%q) = %v, want %v", tc.raw, got, tc.wantYAML)
		}
	}
}

func TestValidateFile(t *testing.T) {
	dir := t.TempDir()

	writeTemp := func(content string) string {
		p := filepath.Join(dir, "test.yaml")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}
		return p
	}

	hpARES, _ := hashpath.Parse("config:server/ares")
	cmdARES := &editCmd{hashPath: hpARES}

	// Valid ARESServerConfig
	validARES := writeTemp(`default-http-port: 8080
servers:
  - address: "ares-1:9090"
    cpu-count: 32
    memory-size: 68719476736
    disk-size: 1099511627776
    cache-size: 549755813888
    enabled: true
`)
	if err := cmdARES.validateFile(validARES); err != nil {
		t.Errorf("expected valid ARES config, got err: %v", err)
	}

	// Invalid YAML syntax
	invalidSyntax := writeTemp("::: invalid yaml :::\n  - [unclosed")
	if err := cmdARES.validateFile(invalidSyntax); err == nil {
		t.Errorf("expected error for invalid YAML syntax, got nil")
	}

	// Unknown field in ARESServerConfig (strict YAML decoding)
	unknownField := writeTemp(`default-http-port: 8080
unknown-key: 123
servers:
  - address: "ares-1:9090"
`)
	if err := cmdARES.validateFile(unknownField); err == nil {
		t.Errorf("expected error for unknown YAML field in ARES config, got nil")
	}

	// Invalid server cpu-count fails ARESServerConfig.Validate()
	invalidServer := writeTemp(`default-http-port: 8080
servers:
  - address: "ares-1:9090"
    cpu-count: 0
`)
	if err := cmdARES.validateFile(invalidServer); err == nil {
		t.Errorf("expected validation error for server with non-positive CPU count, got nil")
	}
}

func TestVimSpecs(t *testing.T) {
	for _, raw := range []string{
		"config:clients/configs",
		"config:server/ares",
		"config:server/buildman",
		"config:git-pusher/pool",
	} {
		hp, err := hashpath.Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q) failed: %v", raw, err)
		}
		c := &editCmd{hashPath: hp}
		ft, specs := c.vimSpecs()
		if ft != "yaml" {
			t.Errorf("vimSpecs(%q) filetype = %q, want 'yaml'", raw, ft)
		}
		if len(specs) == 0 {
			t.Errorf("vimSpecs(%q) returned empty specs", raw)
		}
	}
}
