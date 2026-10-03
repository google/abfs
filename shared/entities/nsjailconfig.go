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

package entities

import (
	"encoding/xml"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

const (
	// NSJailConfigPathEnv names the environment variable holding the path, relative to the
	// top of the source tree, of the NSJail build config XML. If unset, no config is loaded.
	NSJailConfigPathEnv = "ABFS_NSJAIL_CONFIG"
)

// NSJailConfig is the root element
type NSJailConfig struct {
	XMLName        xml.Name                      `xml:"config"`
	Targets        []*NSJailTarget               `xml:"target"`
	Views          []NSJailView                  `xml:"view"` // Keep Views here for unmarshaling
	BuildConfigMap map[string]*NSJailBuildConfig `xml:"-"`
}

// NSJailTarget represents a <target> element
type NSJailTarget struct {
	XMLName         xml.Name               `xml:"target"`
	Name            string                 `xml:"name,attr"`
	Tags            string                 `xml:"tags,attr,omitempty"`
	Overlays        []NSJailOverlay        `xml:"overlay"`
	AllowReadWrites []NSJailAllowReadWrite `xml:"allow_readwrite"`
	BuildConfigs    []*NSJailBuildConfig   `xml:"build_config"`
	Configs         []NSJailConfigElement  `xml:"config"` // For fetch_build, fetch_target inside vf_targets
	Views           []NSJailView           `xml:"view"`   // This will store just a name
}

// NSJailOverlay represents an <overlay> element
type NSJailOverlay struct {
	XMLName xml.Name `xml:"overlay"`
	Name    string   `xml:"name,attr"`
}

// NSJailAllowReadWrite represents an <allow_readwrite> element
type NSJailAllowReadWrite struct {
	XMLName xml.Name `xml:"allow_readwrite"`
	Path    string   `xml:"path,attr"`
}

// NSJailBuildConfig represents a <build_config> element
type NSJailBuildConfig struct {
	XMLName       xml.Name              `xml:"build_config"`
	Name          string                `xml:"name,attr,omitempty"`
	AndroidTarget string                `xml:"android_target,attr,omitempty"`
	Goals         []NSJailGoal          `xml:"goal"`
	Flags         []NSJailFlag          `xml:"flag"`
	Configs       []NSJailConfigElement `xml:"config"` // For fetch_target within build_config
	view          NSJailView            // Pointer to the actual View object, ignored by XML unmarshaler
}

// NSJailGoal represents a <goal> element
type NSJailGoal struct {
	XMLName xml.Name `xml:"goal"`
	Name    string   `xml:"name,attr"`
}

// NSJailFlag represents a <flag> element
type NSJailFlag struct {
	XMLName  xml.Name `xml:"flag"`
	Name     string   `xml:"name,attr"`
	Contexts string   `xml:"contexts,attr,omitempty"` // omitempty if contexts might not be present
}

// NSJailConfigElement represents a <config> element inside a <target> or <build_config>
// Renamed to avoid conflict with the root NSJailConfig struct
type NSJailConfigElement struct {
	XMLName xml.Name `xml:"config"`
	Name    string   `xml:"name,attr,omitempty"`  // For name="fetch_build" or "fetch_target"
	Value   string   `xml:"value,attr,omitempty"` // For value="8061540" or "<target>_partial-userdebug""
}

// NSJailView represents a <view> element
type NSJailView struct {
	XMLName xml.Name     `xml:"view"`
	Name    string       `xml:"name,attr"`
	Paths   []NSJailPath `xml:"path"`
}

// NSJailPath represents a <path> element within a <view>
type NSJailPath struct {
	XMLName     xml.Name `xml:"path"`
	Source      string   `xml:"source,attr"`
	Destination string   `xml:"destination,attr"`
}

// OverlaysForBuildTarget returns the names of each overlay directory for a given build target.
func (n *NSJailConfig) OverlaysForBuildTarget(buildTarget string) []string {
	for _, t := range n.Targets {
		for _, b := range t.BuildConfigs {
			if b.Name == buildTarget {
				var overlays []string
				for _, o := range t.Overlays {
					overlays = append(overlays, o.Name)
				}
				return overlays
			}
		}
	}
	return nil
}

func (n *NSJailConfig) ViewsForBuildTarget(buildTarget string) []string {
	ret := []string{}
	config, ok := n.BuildConfigMap[buildTarget]
	if !ok {
		return nil
	}
	for _, p := range config.view.Paths {
		ret = append(ret, p.Source)
	}
	return ret
}

// LunchableAndroidTarget target will return a lunchable Android target
// given a target.
func (n *NSJailConfig) LunchableAndroidTarget(target string) string {
	for _, t := range n.Targets {
		for _, b := range t.BuildConfigs {
			if b.Name == target || b.AndroidTarget == target {
				return b.AndroidTarget
			}
		}
	}
	return ""
}

// LoadNSJailConfig will load the NSJail config.
func LoadNSJailConfig(mountPoint string) (NSJailConfig, error) {
	var ret NSJailConfig

	relPath := os.Getenv(NSJailConfigPathEnv)
	if relPath == "" {
		return ret, fmt.Errorf("NSJail config not configured (set %s): %w", NSJailConfigPathEnv, os.ErrNotExist)
	}
	if filepath.IsAbs(relPath) {
		return ret, fmt.Errorf("%s must be relative to the source tree, got %q", NSJailConfigPathEnv, relPath)
	}
	configPath := filepath.Join(mountPoint, relPath)
	xmlBytes, err := os.ReadFile(configPath)
	if err != nil {
		return ret, fmt.Errorf("failed to read NSJail config at %s: %w", configPath, err)
	}
	if err := xml.Unmarshal(xmlBytes, &ret); err != nil {
		return ret, fmt.Errorf("failed to unmarshal NSJail config at %s: %w", configPath, err)
	}

	// Post-process the config to populate view pointers.
	viewMap := make(map[string]NSJailView, len(ret.Views))
	buildConfigs := make(map[string]*NSJailBuildConfig)
	for _, v := range ret.Views {
		viewMap[v.Name] = v
	}
	for _, target := range ret.Targets {
		for _, config := range target.BuildConfigs {
			if config.Name == "" {
				if config.AndroidTarget == "" {
					continue
				}
				config.Name = config.AndroidTarget
			}
			if _, ok := buildConfigs[config.Name]; ok {
				return ret, fmt.Errorf("duplicate build config name: %s\n%v/n", config.Name, config)
			}
			if len(target.Views) > 0 && target.Views[0].Name != "" {
				view, ok := viewMap[target.Views[0].Name]
				if !ok {
					log.Printf("View %s not found for target %s", target.Views[0].Name, target.Name)
					continue
				}
				config.view = view
			}
			buildConfigs[config.Name] = config
		}

	}
	ret.BuildConfigMap = buildConfigs
	return ret, nil
}
