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

package ready

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/gitpusher"
	"github.com/google/abfs/manifest/resolve"
	"github.com/google/abfs/remoteconfig"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
)

const (
	reasonNotInPool       = "manifest not in the git-pusher config"
	reasonBranchNotInPool = "branch not in the git-pusher config"
	reasonExcluded        = "excluded by the git-pusher config"
	reasonInvalidRefSpec  = "invalid fetch-ref-spec in the git-pusher config"
	reasonOutsideRefSpec  = "outside the git-pusher fetch-ref-spec"
	reasonPushFailed      = "last push failed"
	reasonNotPushed       = "not pushed yet"
)

// explain replaces each not-found reason with the cause the git-pusher config and the project status give.
// A cause is blocked, so --wait stops, only when no other config entry or git's tag following could still
// push the revision. Without a readable config the reasons stay as they are.
func explain(ctx context.Context, store entities.CASStore, o opts, r *report) {
	if o.pusherConfig == nil {
		return
	}
	pool, err := remoteconfig.ReadConfigFromHP[*gitpusher.GitPusherPoolConfig](ctx, o.pusherConfig, store)
	if err == nil {
		err = pool.Validate()
	}
	if err != nil {
		r.PusherConfigError = fmt.Sprintf("cannot read the git-pusher config %s: %v", o.pusherConfig, err)
		return
	}

	manifestServer := instance.MaybeRemoveScheme(o.manifestServer)
	var repo *gitpusher.RepoConfig
	for _, c := range pool.Repos {
		if c.Manifest.Server == manifestServer && c.Manifest.Project == o.manifestProjectName {
			repo = c
			break
		}
	}
	// The pusher finds the projects of a manifest through every branch and file configured for it.
	branchInPool := repo != nil && repo.ListsManifestFile(o.branch, o.branchFile)
	// With one repo only its config can push a project; with more, the pusher keeps the first one it registers.
	sole := len(pool.Repos) == 1

	statuses := make(map[string]map[string]entities.Hash)
	for i := range r.Unresolved {
		u := &r.Unresolved[i]
		var project, host string
		var refs []string
		switch u.Reason {
		case reasonManifestNotFound:
			project, host, refs = o.manifestProjectName, manifestServer, []string{"refs/heads/" + o.branch}
		case reasonRevisionNotFound:
			project, host, refs = u.Project, instance.Host(u.URL), resolve.Candidates(u.Revision)
		default:
			continue
		}

		standalone := standaloneProject(pool, host, project)
		var fetchConfig gitpusher.FetchConfig
		switch {
		case standalone != nil:
			fetchConfig = standalone.FetchConfig
		case repo == nil:
			u.Reason, u.Blocked = reasonNotInPool, len(pool.Repos) == 0
			continue
		default:
			var excluded bool
			fetchConfig, excluded = repo.ProjectFetchConfig(project)
			if err := fetchConfig.ValidateRefSpecs(); err != nil {
				u.Reason, u.Detail, u.Blocked = reasonInvalidRefSpec, err.Error(), sole
				continue
			}
			if excluded {
				u.Reason, u.Blocked = reasonExcluded, sole
				continue
			}
		}

		if !slices.ContainsFunc(refs, fetchConfig.StoresRef) {
			u.Reason = reasonOutsideRefSpec
			// git also fetches every tag that points into the fetched history.
			if slices.ContainsFunc(refs, func(ref string) bool { return strings.HasPrefix(ref, "refs/tags/") }) {
				u.Detail = "a tag of that name is still pushed if it points into a fetched branch"
			} else {
				u.Blocked = sole && standalone == nil
			}
			continue
		}

		if _, ok := statuses[host]; !ok {
			statuses[host] = statusRefs(store, host)
		}
		statusHash := statuses[host][project]
		if u.Reason == reasonRevisionNotFound && standalone == nil && !branchInPool && statusHash.IsZero() {
			u.Reason, u.Detail = reasonBranchNotInPool, "pushed only if another configured manifest file lists the project"
			continue
		}
		u.Reason, u.Detail = pushStatus(ctx, store, statusHash)
	}
	r.Blocked = slices.ContainsFunc(r.Unresolved, func(u unresolved) bool { return u.Blocked })
}

func standaloneProject(pool *gitpusher.GitPusherPoolConfig, host, project string) *gitpusher.ProjectConfig {
	for _, p := range pool.Projects {
		if p.Server == host && p.Project == project {
			return p
		}
	}
	return nil
}

// statusRefs returns the project status object of every project on a host with one search.
func statusRefs(store entities.CASStore, host string) map[string]entities.Hash {
	hits, err := store.SearchRefs(host, "", binding.ProjectStatusRef, 0)
	if err != nil && !entities.IsNotExist(err) {
		log.Printf("Warning: cannot read the project status on %s: %v", host, err)
	}
	byProject := make(map[string]entities.Hash, len(hits))
	for _, hit := range hits {
		if hit.Ref == binding.ProjectStatusRef && !hit.Hash.IsZero() {
			byProject[hit.Project] = hit.Hash
		}
	}
	return byProject
}

func pushStatus(ctx context.Context, store entities.CASStore, statusHash entities.Hash) (string, string) {
	if statusHash.IsZero() {
		return reasonNotPushed, "no completed push"
	}
	status, err := binding.FetchObject[binding.ProjectStatus](ctx, store, statusHash)
	if err != nil {
		return reasonNotPushed, fmt.Sprintf("cannot read the push status: %v", err)
	}
	if status.LastError != "" {
		return reasonPushFailed, status.LastError
	}
	if status.LastSuccess.IsZero() {
		return reasonNotPushed, "no completed push"
	}
	return reasonNotPushed, "last reported push " + status.LastSuccess.UTC().Format(time.RFC3339)
}
