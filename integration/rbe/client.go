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
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/abfs/cas/fs"

	"github.com/google/abfs/filesystem"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/rbe/model"
)

type Client interface {
	TapResolve(*fs.TapCmdResolvedResult) (*filesystem.RBETapDigestResponse, error)
	GetDigest(relPath string) (*filesystem.RBEPathStat, error)
	SetDigest(relPath string, digest model.Digest, mode uint32) error
	CASFS() bindcas.CASFS
}
type client struct {
	http       *http.Client
	mountPoint string
	casfs      bindcas.CASFS
}

const (
	tapURL       = "http://unix/mnt/get-rbe-tap-digest"
	getDigestURL = "http://unix/mnt/get-rbe-digest"
	setDigestURL = "http://unix/mnt/set-rbe-digest"
)

func NewClient(mountPoint string) (Client, error) {
	absMountPoint, err := filepath.Abs(mountPoint)
	if err != nil {
		return nil, fmt.Errorf("invalid mount point path %q: %w", mountPoint, err)
	}
	if _, err := os.Stat(absMountPoint); err != nil {
		return nil, fmt.Errorf("mount point directory %q does not exist: %w", absMountPoint, err)
	}
	socketPath := filepath.Join(absMountPoint, entities.ABFSSocketFileName)
	if _, err := os.Stat(socketPath); err != nil {
		return nil, fmt.Errorf("unix socket %q not found at mount root: %w", socketPath, err)
	}

	http := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}
	casfs := bindcas.NewExternalCASFS(absMountPoint)
	casfs.SetDefaultTap()

	return &client{http: http, mountPoint: absMountPoint, casfs: casfs}, nil
}
func (c *client) CASFS() bindcas.CASFS {
	return c.casfs
}

func (c *client) GetDigest(relPath string) (*filesystem.RBEPathStat, error) {
	payload := filesystem.GetRBEDigestInput{
		Path: relPath,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GetDigest payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, getDigestURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create GetDigest HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request to get digest failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		return nil, fmt.Errorf("get-rbe-digest endpoint returned status %d: %s", resp.StatusCode, buf.String())
	}

	var pathStat filesystem.RBEPathStat
	if err := json.NewDecoder(resp.Body).Decode(&pathStat); err != nil {
		return nil, fmt.Errorf("failed to decode GetDigest response: %w", err)
	}

	return &pathStat, nil
}
func (c *client) SetDigest(relPath string, digest model.Digest, mode uint32) error {

	payload := filesystem.RBEPathStat{
		Path:   relPath,
		Digest: digest,
		Mode:   mode,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal SetDigest payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, setDigestURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create SetDigest HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request to set digest failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		return fmt.Errorf("set-rbe-digest endpoint returned status %d: %s", resp.StatusCode, buf.String())
	}

	return nil
}
func (c *client) TapResolve(input *fs.TapCmdResolvedResult) (*filesystem.RBETapDigestResponse, error) {

	digestInput := filesystem.RBETapDigestInput{
		Reads:  input.Reads,
		Writes: input.Writes,
	}

	data, err := json.Marshal(digestInput)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal digest request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, tapURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
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
