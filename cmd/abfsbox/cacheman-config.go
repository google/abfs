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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/ninjago"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
)

type CachemanConfigCmd struct {
}

func NewCachemanConfigCmd() *CachemanConfigCmd {
	return &CachemanConfigCmd{}
}

func (c *CachemanConfigCmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:    "cacheman-config",
		Usage:   "[mount-point]",
		Aliases: []string{},
		Desc:    `Display the cacheman configuration for an ABFS mount point.`,
	}
}

func (c *CachemanConfigCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *CachemanConfigCmd) Run(fl *pflag.FlagSet) {
	args := fl.Args()
	mountPoint := "."
	if len(args) > 0 {
		mountPoint = args[0]
	}

	if err := c.run(mountPoint, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func (c *CachemanConfigCmd) run(mountPoint string, out io.Writer) error {
	socketPath, err := FindCachemanSocket(mountPoint)
	if err != nil {
		return err
	}

	cfg, err := ReadCachemanConfig(socketPath)
	if err != nil {
		return err
	}

	if err := cfg.Encode(out); err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}
	return nil
}

// FindCachemanSocket traverses upwards from the given directory to find the cacheman socket
// from mount details (.abfs.mount-details) or ninjago configuration (.ninjago.conf).
func FindCachemanSocket(startDir string) (string, error) {
	absPath, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("cannot resolve absolute path for %q: %w", startDir, err)
	}

	for p := absPath; ; p = filepath.Dir(p) {
		// 1. Try .abfs.mount-details
		if sockPath, err := checkMountDetails(filepath.Join(p, ".abfs.mount-details")); err == nil && sockPath != "" {
			return sockPath, nil
		}

		// 2. Try .ninjago.conf
		if sockPath, err := checkNinjagoConf(filepath.Join(p, ninjago.ConfigFile)); err == nil && sockPath != "" {
			return sockPath, nil
		}

		if p == "/" || p == filepath.Dir(p) {
			break
		}
	}

	return "", entities.NotExistErrf("could not find cacheman socket starting from %q", startDir)
}

func checkMountDetails(detailsPath string) (string, error) {
	f, err := os.Open(detailsPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var m struct {
		Config *instance.Config
	}
	if err := json.NewDecoder(f).Decode(&m); err != nil {
		return "", err
	}

	if m.Config != nil && m.Config.CacheDir != "" {
		sock := filepath.Join(m.Config.CacheDir, "http.socket")
		return sock, nil
	}
	return "", nil
}

func checkNinjagoConf(confPath string) (string, error) {
	cfg, err := ninjago.ReadConfig(confPath)
	if err != nil {
		return "", err
	}
	if cfg.CacheDir != "" {
		sock := filepath.Join(cfg.CacheDir, "http.socket")
		return sock, nil
	}
	return "", nil
}

// ReadCachemanConfig sends a GET /read-config request to the cacheman HTTP unix domain socket.
func ReadCachemanConfig(socketPath string) (*instance.Config, error) {
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}
	resp, err := httpClient.Get("http://localhost/read-config")
	if err != nil {
		return nil, fmt.Errorf("failed to send read-config request to %s: %w", socketPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		var errString string
		if err := json.Unmarshal(body, &errString); err == nil && errString != "" {
			return nil, fmt.Errorf("failed GET /read-config: %d %s %s", resp.StatusCode, resp.Status, errString)
		}
		return nil, fmt.Errorf("failed GET /read-config: %d %s %s", resp.StatusCode, resp.Status, string(body))
	}

	var config instance.Config
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		return nil, fmt.Errorf("failed to decode read-config response: %w", err)
	}
	return &config, nil
}
