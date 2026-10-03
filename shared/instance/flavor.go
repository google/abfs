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
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Support multiple flavors of binaries.

// forceFlavor is used to force the flavor of the binary.
// This is magically filled in through the Makefile. If not set (empty string)
// the file name of the executing binary would be used.
var forceFlavor string

type Flavor struct {
	// The name of the flavor (e.g., "abfs" or "cacheman").
	// It is used to customize help messages and determines the naming of the
	// local directories where config and logs for example are stored (e.g., ".abfs" or ".cacheman").
	// It is possible to use a subflavor, in which case the name would be in the format of <flavor>.<subflavor>.
	Name string

	// The name of the binary.
	BinaryName string

	// The name of the environment variable used to specify the instance name
	// (e.g., ABFS_INSTANCE, CACHEMAN_INSTANCE).
	// Instances can be used to support different projects by keeping each
	// instance's caches and config completely separate.
	InstanceVar string

	// The name of the environment variable used to specify the root directory
	// where all instances live (e.g., ABFS_INSTANCES_DIR).
	// It is the "root" folder where all the different instances are stored.
	InstancesDirVar string

	// The fallback path used if neither the flag nor the environment variable is
	// provided (e.g., "$HOME/.abfs").
	InstancesDirOpt string
}

func (f *Flavor) DashedInstancesDir() string {
	return "--" + f.InstancesDirOpt
}

var (
	ABFSFlavor = Flavor{
		Name:            "abfs",
		InstanceVar:     "ABFS_INSTANCE",
		InstancesDirVar: "ABFS_BASE_DIR",
		InstancesDirOpt: "abfs-base-dir",
	}

	CachemanFlavor = Flavor{
		Name:            "cacheman",
		InstanceVar:     "CACHEMAN_INSTANCE",
		InstancesDirVar: "BASE_DIR",
		InstancesDirOpt: "base-dir",
	}
)

type Instances struct {
	flavor Flavor
}

func DiscoverFlavor() Flavor {
	flavorName := forceFlavor
	if flavorName == "" {
		flavorName = filepath.Base(os.Args[0])
	}
	if strings.HasPrefix(flavorName, "abfs") {
		ABFSFlavor.Name = flavorName
		return ABFSFlavor
	}
	if flavorName == "cacheman" {
		return CachemanFlavor
	}

	log.Printf("unknown flavor %q, defaulting to abfs.", flavorName)
	return ABFSFlavor
}

var DefaultFlavor = DiscoverFlavor()
