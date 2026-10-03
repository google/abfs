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

package instance

import (
	"slices"
)

const ClientsConfigsHashPath = "config:clients/configs"

// ConfigRemoteOverrides is a list of overrides for the remote config.
// To resolve a remote override, the client starts with an empty config and applies matching
// overrides in order. This allows later overrides to override earlier ones.
type ConfigRemoteOverrides struct {
	Overrides []ClientsOverrides `yaml:",inline" json:""`
}

func (c *ConfigRemoteOverrides) UnmarshalYAML(unmarshal func(interface{}) error) error {
	return unmarshal(&c.Overrides)
}

func (c *ConfigRemoteOverrides) Validate() error {
	return nil
}

type ClientsSelect struct {
	Classes []string `yaml:"classes,omitempty" json:",omitempty"`
	Users   []string `yaml:"users,omitempty" json:",omitempty"`
	//TODO: add groups once we introduce groups in abfs
	MinVersion string `yaml:"min_version,omitempty" json:",omitempty"`
	MaxVersion string `yaml:"max_version,omitempty" json:",omitempty"`
}

type ClientsOverrides struct {
	Select ClientsSelect  `yaml:"select" json:""`
	Config ConfigOptional `yaml:"config" json:""`
}

type ClientInfo struct {
	User    string
	Class   string
	Version string
}

func (c *ClientsOverrides) Matches(info ClientInfo) bool {
	if c.Select.MinVersion != "" && info.Version < c.Select.MinVersion {
		return false
	}
	if c.Select.MaxVersion != "" && info.Version > c.Select.MaxVersion {
		return false
	}

	if !matchesList(c.Select.Classes, info.Class) {
		return false
	}

	if !matchesList(c.Select.Users, info.User) {
		return false
	}

	return true
}

func matchesList(items []string, item string) bool {
	if len(items) == 0 {
		return true
	}

	return slices.Contains(items, item)
}

func ResolveRemoteConfig(clientInfo ClientInfo, overrides *ConfigRemoteOverrides) *ConfigOptional {
	config := new(ConfigOptional)
	for _, override := range overrides.Overrides {
		if override.Matches(clientInfo) {
			config.Apply(&override.Config)
		}
	}
	return config
}
