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

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/manifest"
)

func TestMarshalXMLRPCRequest(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		args     []string
		expected string
	}{
		{
			name:     "no args",
			method:   "SomeMethod",
			args:     nil,
			expected: `<?xml version="1.0"?><methodCall><methodName>SomeMethod</methodName><params></params></methodCall>`,
		},
		{
			name:     "one arg",
			method:   "SomeMethod",
			args:     []string{"arg1"},
			expected: `<?xml version="1.0"?><methodCall><methodName>SomeMethod</methodName><params><param><value><string>arg1</string></value></param></params></methodCall>`,
		},
		{
			name:     "two args with escaping",
			method:   "SomeMethod",
			args:     []string{"arg1", "arg2 <>&\"'"},
			expected: `<?xml version="1.0"?><methodCall><methodName>SomeMethod</methodName><params><param><value><string>arg1</string></value></param><param><value><string>arg2 &lt;&gt;&amp;&#34;&#39;</string></value></param></params></methodCall>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := marshalXMLRPCRequest(tt.method, tt.args...)
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestExtractManifestResponse(t *testing.T) {
	tests := []struct {
		name            string
		xml             string
		expectedSuccess bool
		expectedXML     string
		expectError     bool
	}{
		{
			name: "success true",
			xml: `<?xml version="1.0"?>
<methodResponse>
  <params>
    <param>
      <value>
        <array>
          <data>
            <value><boolean>1</boolean></value>
            <value><string>&lt;manifest&gt;&lt;/manifest&gt;</string></value>
          </data>
        </array>
      </value>
    </param>
  </params>
</methodResponse>`,
			expectedSuccess: true,
			expectedXML:     "<manifest></manifest>",
			expectError:     false,
		},
		{
			name: "success false with error message",
			xml: `<?xml version="1.0"?>
<methodResponse>
  <params>
    <param>
      <value>
        <array>
          <data>
            <value><boolean>0</boolean></value>
            <value><string>build not found</string></value>
          </data>
        </array>
      </value>
    </param>
  </params>
</methodResponse>`,
			expectedSuccess: false,
			expectedXML:     "build not found",
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp xmlrpcResponse
			err := xml.Unmarshal([]byte(tt.xml), &resp)
			require.NoError(t, err)

			success, manifestXML, err := extractManifestResponse(&resp)
			if (err != nil) != tt.expectError {
				t.Fatalf("expected error: %v, got: %v", tt.expectError, err)
			}
			if !tt.expectError {
				if success != tt.expectedSuccess {
					t.Errorf("expected success %v, got %v", tt.expectedSuccess, success)
				}
				if manifestXML != tt.expectedXML {
					t.Errorf("expected xml %q, got %q", tt.expectedXML, manifestXML)
				}
			}
		})
	}
}

func TestGetSyncTarget(t *testing.T) {
	// Helper to clear env
	clearEnv := func() {
		require.NoError(t, os.Unsetenv("SYNC_TARGET"))
		require.NoError(t, os.Unsetenv("TARGET_PRODUCT"))
		require.NoError(t, os.Unsetenv("TARGET_BUILD_VARIANT"))
		require.NoError(t, os.Unsetenv("TARGET_RELEASE"))
	}

	tests := []struct {
		name     string
		env      map[string]string
		expected string
	}{
		{
			name:     "nothing set",
			env:      nil,
			expected: "",
		},
		{
			name: "SYNC_TARGET takes precedence",
			env: map[string]string{
				"SYNC_TARGET":          "sync-target-value",
				"TARGET_PRODUCT":       "prod",
				"TARGET_BUILD_VARIANT": "var",
			},
			expected: "sync-target-value",
		},
		{
			name: "product and variant and release",
			env: map[string]string{
				"TARGET_PRODUCT":       "prod",
				"TARGET_BUILD_VARIANT": "var",
				"TARGET_RELEASE":       "rel",
			},
			expected: "prod-rel-var",
		},
		{
			name: "product and variant only",
			env: map[string]string{
				"TARGET_PRODUCT":       "prod",
				"TARGET_BUILD_VARIANT": "var",
			},
			expected: "prod-var",
		},
		{
			name: "incomplete product/variant",
			env: map[string]string{
				"TARGET_PRODUCT": "prod",
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv()
			for k, v := range tt.env {
				require.NoError(t, os.Setenv(k, v))
			}
			defer clearEnv()

			got := getSyncTarget()
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestFetchSmartSyncManifest(t *testing.T) {
	t.Setenv("PATH", "")
	// Spin up a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers
		if r.Header.Get("Content-Type") != "text/xml" {
			t.Errorf("expected Content-Type text/xml, got %s", r.Header.Get("Content-Type"))
		}

		// Read body
		body, _ := io.ReadAll(r.Body)
		expectedReq := marshalXMLRPCRequest("GetApprovedManifest", "master", "target-value")
		if string(body) != expectedReq {
			t.Errorf("expected request %q, got %q", expectedReq, string(body))
		}

		// Respond with success
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusOK)
		_, err := fmt.Fprint(w, `<?xml version="1.0"?>
<methodResponse>
  <params>
    <param>
      <value>
        <array>
          <data>
            <value><boolean>1</boolean></value>
            <value><string>&lt;manifest&gt;&lt;/manifest&gt;</string></value>
          </data>
        </array>
      </value>
    </param>
  </params>
</methodResponse>`)
		require.NoError(t, err)
	}))
	defer server.Close()

	r := &Repo{} // Empty repo is fine for callXMLRPC
	opts := SyncOpts{}

	// We need to construct the request body manually because fetchSmartSyncManifest uses getSyncTarget from env
	require.NoError(t, os.Setenv("SYNC_TARGET", "target-value"))
	defer func() { _ = os.Unsetenv("SYNC_TARGET") }()

	r.manifestBranch = "refs/heads/master"

	manifestXML, err := r.fetchSmartSyncManifest(t.Context(), server.URL, "", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedXML := "<manifest></manifest>"
	if manifestXML != expectedXML {
		t.Errorf("expected manifest %q, got %q", expectedXML, manifestXML)
	}
}

func TestCallManifestRPC_Auth(t *testing.T) {
	t.Setenv("PATH", "")
	username := "testuser"
	password := "testpass"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != username || p != password {
			t.Errorf("expected basic auth %s:%s, got %s:%s (ok=%v)", username, password, u, p, ok)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusOK)
		_, err := fmt.Fprint(w, `<?xml version="1.0"?>
<methodResponse>
  <params>
    <param>
      <value>
        <array>
          <data>
            <value><boolean>1</boolean></value>
            <value><string>ok</string></value>
          </data>
        </array>
      </value>
    </param>
  </params>
</methodResponse>`)
		require.NoError(t, err)
	}))
	defer server.Close()

	// Test auth via opts
	opts := SyncOpts{
		ManifestServerUsername: username,
		ManifestServerPassword: password,
	}
	_, err := callManifestRPC(t.Context(), server.URL, "", "req", opts)
	if err != nil {
		t.Errorf("unexpected error with opts auth: %v", err)
	}

	// Test auth via URL
	parsedURL, _ := url.Parse(server.URL)
	parsedURL.User = url.UserPassword(username, password)

	_, err = callManifestRPC(t.Context(), parsedURL.String(), "", "req", SyncOpts{})
	if err != nil {
		t.Errorf("unexpected error with URL auth: %v", err)
	}
}

func TestManifestNotice(t *testing.T) {
	r := &Repo{}
	if r.ManifestNotice() != "" {
		t.Errorf("expected empty notice for nil flatMan")
	}
	r.flatMan = &manifest.Manifest{}
	if r.ManifestNotice() != "" {
		t.Errorf("expected empty notice for nil Notice")
	}
	notice := manifest.Notice("test notice")
	r.flatMan.Notice = &notice
	if r.ManifestNotice() != "test notice" {
		t.Errorf("expected 'test notice', got %q", r.ManifestNotice())
	}
}

func TestCallManifestRPC_Errors(t *testing.T) {
	t.Setenv("PATH", "")
	tests := []struct {
		name        string
		statusCode  int
		contentType string
		response    string
		expectedErr string
	}{
		{
			name:        "http error",
			statusCode:  http.StatusInternalServerError,
			contentType: "text/xml",
			response:    "internal server error",
			expectedErr: "XML-RPC request failed with status: 500 Internal Server Error",
		},
		{
			name:        "invalid xml",
			statusCode:  http.StatusOK,
			contentType: "text/xml",
			response:    "invalid xml",
			expectedErr: "failed to unmarshal XML-RPC response",
		},
		{
			name:        "fault response",
			statusCode:  http.StatusOK,
			contentType: "text/xml",
			response: `<?xml version="1.0"?>
<methodResponse>
  <fault>
    <value>
      <struct>
        <member>
          <name>faultCode</name>
          <value><int>4</int></value>
        </member>
        <member>
          <name>faultString</name>
          <value><string>Too many parameters.</string></value>
        </member>
      </struct>
    </value>
  </fault>
</methodResponse>`,
			expectedErr: "XML-RPC fault: Too many parameters.",
		},
		{
			name:        "missing params",
			statusCode:  http.StatusOK,
			contentType: "text/xml",
			response: `<?xml version="1.0"?>
<methodResponse>
</methodResponse>`,
			expectedErr: "invalid XML-RPC response: missing params",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.statusCode)
				_, _ = fmt.Fprint(w, tt.response)
			}))
			defer server.Close()

			_, err := callManifestRPC(t.Context(), server.URL, "", "req", SyncOpts{})
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func TestSmartSyncProxy(t *testing.T) {
	ctx := t.Context()

	t.Run("no helper", func(t *testing.T) {
		t.Setenv("PATH", "")
		proxy, err := smartSyncProxy(ctx, "https://example.com/manifest", "")
		require.NoError(t, err)
		require.Nil(t, proxy)
	})

	t.Run("helper returns proxy URL", func(t *testing.T) {
		t.Setenv("PATH", "")
		tmpDir := t.TempDir()
		helperPath := filepath.Join(tmpDir, "helper.sh")
		script := "#!/bin/sh\necho '{\"status\":\"ok\",\"message\":\"http://proxy.example.com:8080\"}'\n"
		require.NoError(t, os.WriteFile(helperPath, []byte(script), 0755))

		proxy, err := smartSyncProxy(ctx, "https://example.com/manifest", helperPath)
		require.NoError(t, err)
		require.NotNil(t, proxy)
		require.Equal(t, "http://proxy.example.com:8080", proxy.String())
	})

	t.Run("helper returns empty message", func(t *testing.T) {
		t.Setenv("PATH", "")
		tmpDir := t.TempDir()
		helperPath := filepath.Join(tmpDir, "helper.sh")
		script := "#!/bin/sh\necho '{\"status\":\"ok\",\"message\":\"\"}'\n"
		require.NoError(t, os.WriteFile(helperPath, []byte(script), 0755))

		proxy, err := smartSyncProxy(ctx, "https://example.com/manifest", helperPath)
		require.NoError(t, err)
		require.Nil(t, proxy)
	})

	t.Run("helper returns error status", func(t *testing.T) {
		t.Setenv("PATH", "")
		tmpDir := t.TempDir()
		helperPath := filepath.Join(tmpDir, "helper.sh")
		script := "#!/bin/sh\necho '{\"status\":\"error\",\"message\":\"auth failed\"}'\n"
		require.NoError(t, os.WriteFile(helperPath, []byte(script), 0755))

		_, err := smartSyncProxy(ctx, "https://example.com/manifest", helperPath)
		require.Error(t, err)
		require.Contains(t, err.Error(), "helper returned error error: auth failed")
	})
}
