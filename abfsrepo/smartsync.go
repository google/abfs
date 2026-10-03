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
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/google/abfs/shared/entities"
)

// XML-RPC structs
type xmlrpcResponse struct {
	XMLName xml.Name      `xml:"methodResponse"`
	Params  *xmlrpcParams `xml:"params,omitempty"`
	Fault   *xmlrpcFault  `xml:"fault,omitempty"`
}

type xmlrpcParams struct {
	Param xmlrpcParam `xml:"param"`
}

type xmlrpcParam struct {
	Value xmlrpcValue `xml:"value"`
}

type xmlrpcValue struct {
	Array    *xmlrpcArray  `xml:"array,omitempty"`
	Struct   *xmlrpcStruct `xml:"struct,omitempty"`
	String   string        `xml:"string,omitempty"`
	Boolean  string        `xml:"boolean,omitempty"`
	Int      string        `xml:"int,omitempty"`
	RawValue string        `xml:",chardata"`
}

type xmlrpcArray struct {
	Data []xmlrpcValue `xml:"data>value"`
}

type xmlrpcStruct struct {
	Member []xmlrpcMember `xml:"member"`
}

type xmlrpcMember struct {
	Name  string      `xml:"name"`
	Value xmlrpcValue `xml:"value"`
}

type xmlrpcFault struct {
	Value xmlrpcValue `xml:"value"`
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(s)); err != nil {
		return s // fallback
	}
	return buf.String()
}

func callXMLRPC(ctx context.Context, serverURL string, helper string, requestBody string, username, password string) (xmlResp *xmlrpcResponse, retErr error) {
	req, err := http.NewRequestWithContext(ctx, "POST", serverURL, strings.NewReader(requestBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml")

	if username != "" || password != "" {
		req.SetBasicAuth(username, password)
	}

	proxy, err := smartSyncProxy(ctx, serverURL, helper)
	if err != nil {
		return nil, fmt.Errorf("failed to get smart sync proxy: %w", err)
	}

	client := http.DefaultClient
	if proxy != nil {
		client = &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxy),
			},
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("XML-RPC request failed: %w", err)
	}
	defer entities.CloseErrCheck(resp.Body, &retErr)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("XML-RPC request failed with status: %s", resp.Status)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read XML-RPC response: %w", err)
	}

	xmlResp = new(xmlrpcResponse)
	if err := xml.Unmarshal(respBody, &xmlResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal XML-RPC response: %w, response was %q", err, respBody)
	}

	if xmlResp.Fault != nil {
		faultStr := "unknown XML-RPC fault"
		if xmlResp.Fault.Value.Struct != nil {
			for _, m := range xmlResp.Fault.Value.Struct.Member {
				if m.Name == "faultString" {
					faultStr = m.Value.String
					if faultStr == "" {
						faultStr = m.Value.RawValue
					}
				}
			}
		}
		return nil, fmt.Errorf("XML-RPC fault: %s", faultStr)
	}

	if xmlResp.Params == nil {
		return nil, fmt.Errorf("invalid XML-RPC response: missing params")
	}
	return xmlResp, nil
}

func smartSyncProxy(ctx context.Context, serverURL, helper string) (*url.URL, error) {
	if helper != "" {
		return smartSyncProxyFromHelper(ctx, serverURL, helper)
	}
	return nil, nil
}

func runWithOutput(cmd *exec.Cmd) (string, error) {
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("command %q %s failed: %w\n%s", cmd.Path, cmd.Args, err, out)
	}
	return string(out), nil
}

func smartSyncProxyFromHelper(ctx context.Context, serverURL, helper string) (*url.URL, error) {
	output, err := runWithOutput(exec.CommandContext(ctx, helper, serverURL))
	if err != nil {
		return nil, fmt.Errorf("failed to run helper `%s %s`: %w", helper, serverURL, err)
	}
	var resp struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal helper response: %w", err)
	}
	if resp.Status != "ok" {
		return nil, fmt.Errorf("helper returned error %s: %s", resp.Status, resp.Message)
	}

	if resp.Message == "" {
		return nil, nil
	}
	return url.Parse(resp.Message)
}

func marshalXMLRPCRequest(method string, args ...string) string {
	var sb strings.Builder
	fmt.Fprint(&sb, `<?xml version="1.0"?><methodCall>`)
	fmt.Fprintf(&sb, "<methodName>%s</methodName><params>", method)
	for _, arg := range args {
		fmt.Fprintf(&sb, "<param><value><string>%s</string></value></param>", xmlEscape(arg))
	}
	fmt.Fprint(&sb, "</params></methodCall>")
	return sb.String()
}

func extractManifestResponse(resp *xmlrpcResponse) (bool, string, error) {
	arr := resp.Params.Param.Value.Array
	if arr == nil || len(arr.Data) < 2 {
		return false, "", fmt.Errorf("invalid XML-RPC response: expected array of at least 2 elements")
	}

	successVal := arr.Data[0]
	manifestVal := arr.Data[1]

	success := false
	valStr := successVal.Boolean
	if valStr == "" {
		valStr = successVal.RawValue
	}
	valStr = strings.TrimSpace(valStr)
	if valStr == "1" || strings.ToLower(valStr) == "true" {
		success = true
	}

	manifestXML := manifestVal.String
	if manifestXML == "" {
		manifestXML = manifestVal.RawValue
	}

	return success, manifestXML, nil
}

func (r *Repo) fetchSmartSyncManifest(ctx context.Context, serverURL, helper string, opts SyncOpts) (string, error) {
	branch := strings.TrimPrefix(r.manifestBranch, "refs/heads/")
	target := getSyncTarget()

	var requestBody string
	if target != "" {
		requestBody = marshalXMLRPCRequest("GetApprovedManifest", branch, target)
	} else {
		requestBody = marshalXMLRPCRequest("GetApprovedManifest", branch)
	}

	return callManifestRPC(ctx, serverURL, helper, requestBody, opts)
}

func (r *Repo) fetchSmartTagManifest(ctx context.Context, serverURL, helper string, opts SyncOpts) (string, error) {
	requestBody := marshalXMLRPCRequest("GetManifest", opts.SmartTag)
	return callManifestRPC(ctx, serverURL, helper, requestBody, opts)
}

func callManifestRPC(ctx context.Context, serverURL, helper string, requestBody string, opts SyncOpts) (string, error) {
	username := opts.ManifestServerUsername
	password := opts.ManifestServerPassword

	if username == "" && password == "" {
		u, err := url.Parse(serverURL)
		if err == nil && u.User != nil {
			username = u.User.Username()
			password, _ = u.User.Password()
		}
	}

	xmlResp, err := callXMLRPC(ctx, serverURL, helper, requestBody, username, password)
	if err != nil {
		return "", err
	}

	success, manifestXML, err := extractManifestResponse(xmlResp)
	if err != nil {
		return "", err
	}

	if !success {
		return "", fmt.Errorf("manifest server returned failure (error message might be: %s)", manifestXML)
	}

	return manifestXML, nil
}

func (r *Repo) applySmartManifest(ctx context.Context, opts SyncOpts) error {
	if r.flatMan.ManifestServer == nil || r.flatMan.ManifestServer.URL == "" {
		return fmt.Errorf("error: manifest server not defined in manifest")
	}
	serverURL := r.flatMan.ManifestServer.URL
	helper := r.flatMan.ManifestServer.Helper

	var manifestXML string
	var err error
	if opts.SmartSync {
		manifestXML, err = r.fetchSmartSyncManifest(ctx, serverURL, helper, opts)
	} else if opts.SmartTag != "" {
		manifestXML, err = r.fetchSmartTagManifest(ctx, serverURL, helper, opts)
	}
	if err != nil {
		return err
	}

	// Write to .repo/manifests/smart_sync_override.xml
	overridePath := path.Join(r.manifestsDir, "smart_sync_override.xml")
	if err := os.WriteFile(overridePath, []byte(manifestXML), 0644); err != nil {
		return fmt.Errorf("failed to write smart manifest: %w", err)
	}

	return nil
}

func getSyncTarget() string {
	if t := os.Getenv("SYNC_TARGET"); t != "" {
		return t
	}
	product := os.Getenv("TARGET_PRODUCT")
	variant := os.Getenv("TARGET_BUILD_VARIANT")
	release := os.Getenv("TARGET_RELEASE")

	if product != "" && variant != "" {
		if release != "" {
			return fmt.Sprintf("%s-%s-%s", product, release, variant)
		}
		return fmt.Sprintf("%s-%s", product, variant)
	}
	return ""
}
