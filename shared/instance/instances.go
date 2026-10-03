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
	"fmt"
	"os"
	"path"
	"path/filepath"
)

const (
	DefaultInstanceName = "default"
	DefaultBaseDir      = "$HOME/.abfs"
)

func NewInstances(flavor Flavor) *Instances {
	return &Instances{
		flavor: flavor,
	}
}

func (i *Instances) ResolveName(instanceOverride string) string {
	if instanceOverride != "" {
		return instanceOverride
	}

	if envInstance := os.Getenv(i.flavor.InstanceVar); envInstance != "" {
		return envInstance
	}

	return DefaultInstanceName
}

func (i *Instances) ResolveBaseDir(baseDirOverride string) string {
	var dir string

	if baseDirOverride != "" {
		dir = baseDirOverride
	}

	if dir == "" {
		if envInstance := os.Getenv(i.flavor.InstancesDirVar); envInstance != "" {
			dir = envInstance
		}
	}

	if dir == "" {
		dir = os.ExpandEnv(DefaultBaseDir)
	}

	return AbsPath(dir)
}

func AbsPath(inputMntPt string) string {
	mntPt, err := filepath.Abs(inputMntPt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid path %s: %v", inputMntPt, err)
		os.Exit(1)
	}
	return mntPt
}

func (i *Instances) ResolveNewInstance(instanceOverride, baseDirOverride string, flagsConfig *ConfigOptional, kvConfigs []string) *Instance {
	name := i.ResolveName(instanceOverride)

	baseDir := i.ResolveBaseDir(baseDirOverride)
	instanceDir := filepath.Join(baseDir, name)

	if err := os.Setenv(i.flavor.InstanceVar, name); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: Could not set %s environment variable: %v\n", i.flavor.InstanceVar, err)
		os.Exit(1)
	}
	if err := os.Setenv(i.flavor.InstancesDirVar, baseDir); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: Could not set %s environment variable: %v\n", i.flavor.InstancesDirVar, err)
		os.Exit(1)
	}

	if err := flagsConfig.ApplyKeyValues(kvConfigs); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: Could not apply key-value config: %v\n", err)
		os.Exit(1)
	}
	return NewInstance(name, instanceDir, baseDir, path.Base(os.Args[0]), flagsConfig, i.flavor)
}
