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

package buildman

import "fmt"

const ConfigBuildmanNodesHashPath = "config:server/buildman"

type BuildmanNode struct {
	Name     string `yaml:"name"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Inactive bool   `yaml:"inactive"`
}

type BuildmanNodeConfig struct {
	DefaultPort int            `yaml:"default-port"`
	Nodes       []BuildmanNode `yaml:"nodes"`
}

func (c BuildmanNodeConfig) Validate() error {
	for i := range c.Nodes {
		node := &c.Nodes[i]
		if node.Name == "" {
			return fmt.Errorf("node at index %d has empty name", i)
		}
		if node.Host == "" {
			node.Host = node.Name
		}
		if node.Port == 0 {
			node.Port = c.DefaultPort
		}
	}
	return nil
}
