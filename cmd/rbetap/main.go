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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/google/abfs/cas/fs"

	"github.com/google/abfs/filesystem"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/version"
)

type RBETapResult struct {
	Reads   []filesystem.RBEPathStat `json:"reads"`
	Writes  []filesystem.RBEPathStat `json:"writes"`
	Deletes map[string]struct{}      `json:"deletes"`
	fs.TapInfo
}

func formatSisoTapData(readsMap map[string]bindcas.CASBackendStat, writesMap map[string]bindcas.CASBackendStat, deletesMap map[string]struct{}, absDir string) map[string][]string {
	reads := make([]string, 0, len(readsMap))
	for p := range readsMap {
		reads = append(reads, filepath.Join(absDir, p))
	}
	sort.Strings(reads)

	writes := make([]string, 0, len(writesMap))
	for p := range writesMap {
		writes = append(writes, filepath.Join(absDir, p))
	}
	sort.Strings(writes)

	deletes := make([]string, 0, len(deletesMap))
	for p := range deletesMap {
		deletes = append(deletes, filepath.Join(absDir, p))
	}
	sort.Strings(deletes)

	m := make(map[string][]string)
	if len(reads) > 0 {
		m["reads"] = reads
	}
	if len(writes) > 0 {
		m["writes"] = writes
	}
	if len(deletes) > 0 {
		m["deletes"] = deletes
	}
	return m
}

func sendRBETapDigest(client *http.Client, url string, input filesystem.RBETapDigestInput) (*filesystem.RBETapDigestResponse, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal digest request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request to mount failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		return nil, fmt.Errorf("mount endpoint returned status %d: %s", resp.StatusCode, buf.String())
	}

	var digestResp filesystem.RBETapDigestResponse
	if err := json.NewDecoder(resp.Body).Decode(&digestResp); err != nil {
		return nil, fmt.Errorf("failed to decode digest response: %w", err)
	}

	return &digestResp, nil
}

func main() {
	var (
		mountPoint string
		dir        string
		tapOutput  string
		pretty     bool
		stdin      bool
		showVer    bool
	)

	// in soong, this command will be invoked by siso in nsjail,
	// which mount workspace into /src as read only.
	// mountPoint should be original path, outside of nsjail,
	// otherwise tap would fail with
	//  failed to open tmp file for dir /src/.ctrl/tap (read-only file system)
	flag.StringVar(&mountPoint, "mountPoint", "", "Path to the ABFS/CASFS mount point")
	// siso expects absolute path in current namespace (in nsjail),
	// resolvedTapResult would contain path in mount point
	// e.g. "/out/soong/...", but siso expects "/src/out/soong/...".
	// "." will be resolved to "/src" in the namespace.
	flag.StringVar(&dir, "dir", ".", "Current directory relative to mount point in the current namespace")
	flag.StringVar(&tapOutput, "tap_output", "", "Output path for tap result JSON (SISO_TAP_COMMAND interface)")
	flag.BoolVar(&pretty, "pretty", false, "Pretty print JSON output")
	flag.BoolVar(&stdin, "stdin", false, "Pass stdin to the underlying command process")
	flag.BoolVar(&showVer, "version", false, "Print application version")
	flag.Parse()

	if showVer {
		fmt.Println(version.Version())
		return
	}
	if tapOutput != "" && mountPoint == "" {
		mountPoint = os.Getenv("ABFS_MOUNTDIR")
	}
	// 1. Require --mountPoint explicitly
	if mountPoint == "" {
		log.Fatalf("Error: --mountPoint is required\n")
	}

	absMountPoint, err := filepath.Abs(mountPoint)
	if err != nil {
		log.Fatalf("Error: invalid mount point path %q: %v\n", mountPoint, err)
	}

	if _, err := os.Stat(absMountPoint); err != nil {
		log.Fatalf("Error: mount point directory %q does not exist: %v\n", absMountPoint, err)
	}

	// 2. Validate abfs.sock at mount root
	socketPath := filepath.Join(absMountPoint, entities.ABFSSocketFileName)
	if _, err := os.Stat(socketPath); err != nil {
		log.Fatalf("Error: unix socket %q not found at mount root: %v\n", socketPath, err)
	}

	if len(flag.Args()) == 0 {
		log.Fatalf("Error: No command provided to tap\n")
	}

	cmd := exec.Command(flag.Args()[0], flag.Args()[1:]...)
	if stdin {
		cmd.Stdin = os.Stdin
	}
	cas := bindcas.NewExternalCASFS(absMountPoint)
	cas.SetDefaultTap()

	// 3. Run local filesystem tap via bindcas.TapResolved
	resolvedTapResult, err := cas.TapResolved(cmd)
	if err != nil {
		log.Fatalf("Failed to run bindcas.TapResolved mountPoint=%q: %v\n", mountPoint, err)
	}

	// SISO_TAP_COMMAND interface mode when --tap_output is specified
	if tapOutput != "" {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			log.Fatalf("Error: invalid dir %q: %v\n", dir, err)
		}
		if resolvedTapResult.Stdout != "" {
			os.Stdout.WriteString(resolvedTapResult.Stdout)
		}
		if resolvedTapResult.Stderr != "" {
			os.Stderr.WriteString(resolvedTapResult.Stderr)
		}

		tapData := formatSisoTapData(resolvedTapResult.Reads, resolvedTapResult.Writes, resolvedTapResult.Deletes, absDir)
		var buf []byte
		var berr error
		if pretty {
			buf, berr = json.MarshalIndent(tapData, "", "  ")
		} else {
			buf, berr = json.Marshal(tapData)
		}
		if berr != nil {
			log.Fatalf("Failed to marshal tap data: %v\n", berr)
		}
		if err := os.WriteFile(tapOutput, buf, 0644); err != nil {
			log.Fatalf("Failed to save tap output %q: %v\n", tapOutput, err)
		}

		if resolvedTapResult.ExitCode != 0 {
			os.Exit(resolvedTapResult.ExitCode)
		}
		return
	}

	// Original RBE digest resolution mode (when --tap_output is not specified)
	// 4. Create HTTP client configured for Unix domain socket
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}

	digestInput := filesystem.RBETapDigestInput{
		Reads:  resolvedTapResult.Reads,
		Writes: resolvedTapResult.Writes,
	}

	endpointURL := "http://unix/mnt/get-rbe-tap-digest"

	// 5. Post to HTTP mount endpoint to convert Reads/Writes to SHA-256
	digestResp, err := sendRBETapDigest(client, endpointURL, digestInput)
	if err != nil {
		log.Fatalf("Failed to resolve RBE tap digest via mount: %v\n", err)
	}

	// 6. Stitch output together: SHA-256 Reads/Writes + Deletes + TapInfo
	output := RBETapResult{
		Reads:   digestResp.Reads,
		Writes:  digestResp.Writes,
		Deletes: resolvedTapResult.Deletes,
		TapInfo: resolvedTapResult.TapInfo,
	}

	// 7. Encode final JSON output
	enc := json.NewEncoder(os.Stdout)
	if pretty {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(output); err != nil {
		log.Fatalf("JSON encoding failure: %v\n", err)
	}
}
