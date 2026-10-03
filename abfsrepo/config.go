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
	"strings"
)

const (
	ConfigManifestGroups    = "manifest.groups"
	ConfigSyncRemoteURL     = "repo.syncstate.remote.origin.url"
	ConfigSyncDefaultRemote = "repo.syncstate.branch.default.remote"
	ConfigSyncDefaultMerge  = "repo.syncstate.branch.default.merge"
)

func (r *Repo) loadConfig() error {
	groupsStr, err := r.manifestProject.GetConfig(ConfigManifestGroups, "")
	if err != nil {
		return err
	}
	var groups []string
	if len(groupsStr) > 0 {
		groups = strings.Split(groupsStr, ",")
	}
	r.groupsInclude, r.groupsExclude = FilterGroups(groups)

	manifestURL, err := r.manifestProject.GetConfig(ConfigSyncRemoteURL, "")
	if err != nil {
		return err
	}
	r.manifestURL = manifestURL

	manifestBranch, err := r.manifestProject.GetConfig(ConfigSyncDefaultMerge, "")
	if err != nil {
		return err
	}
	r.manifestBranch = manifestBranch

	manifestRemote, err := r.manifestProject.GetConfig(ConfigSyncDefaultRemote, "")
	if err != nil {
		return err
	}
	r.manifestRemote = manifestRemote

	return nil
}
