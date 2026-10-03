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

package grpc

import "fmt"

const ConfigARESServersHashPath = "config:server/ares"

type ARESServer struct {
	Address    string `yaml:"address"`
	CPUCount   int    `yaml:"cpu-count"`
	MemorySize int64  `yaml:"memory-size"`
	DiskSize   int64  `yaml:"disk-size"`
	CacheSize  int64  `yaml:"cache-size"`
	Enabled    bool   `yaml:"enabled"`
	HTTPPort   int    `yaml:"http-port"`
}

type ARESServerConfig struct {
	DefaultHTTPPort int          `yaml:"default-http-port"`
	Servers         []ARESServer `yaml:"servers"`
}

func (c ARESServerConfig) Validate() error {
	if len(c.Servers) == 0 {
		return nil
	}
	for i := range c.Servers {
		server := &c.Servers[i]
		if server.Address == "" {
			return fmt.Errorf("server at index %d has empty address", i)
		}
		if server.CPUCount <= 0 {
			return fmt.Errorf("server %q has non-positive CPU count: %d", server.Address, server.CPUCount)
		}
		if server.MemorySize <= 0 {
			return fmt.Errorf("server %q has non-positive memory size: %d", server.Address, server.MemorySize)
		}
		if server.DiskSize <= 0 {
			return fmt.Errorf("server %q has non-positive disk size: %d", server.Address, server.DiskSize)
		}
		if server.CacheSize <= 0 {
			return fmt.Errorf("server %q has non-positive cache size: %d", server.Address, server.CacheSize)
		}
		if server.HTTPPort == 0 {
			server.HTTPPort = c.DefaultHTTPPort
		}
	}
	return nil
}
