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

package resolve

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
)

// RefSearcher looks refs up by prefix, as entities.CASStore does.
type RefSearcher interface {
	SearchRefs(server, project, refPrefix string, limit int) ([]entities.ProjectRefHash, error)
}

// Request is the revision of one manifest project.
type Request struct {
	Project  string
	FetchURL string
	Revision string
}

// Result is the ref a revision resolved to, empty for a commit hash, and its commit, zero when not found.
type Result struct {
	Ref  string
	Hash entities.Hash
}

// UnresolvedError names every request whose revision was not found.
type UnresolvedError struct {
	Requests []Request
}

var _ error = (*UnresolvedError)(nil)

func (e *UnresolvedError) Error() string {
	lines := make([]string, len(e.Requests))
	for i, r := range e.Requests {
		lines[i] = fmt.Sprintf("cannot resolve %q for project %q at URL %q", r.Revision, r.Project, r.FetchURL)
	}
	return strings.Join(lines, "\n")
}

// Candidates returns the refs a revision can name: itself for a full ref, else the branch, as repo reads it, then the tag.
func Candidates(revision string) []string {
	if strings.HasPrefix(revision, "refs/") {
		return []string{revision}
	}
	return []string{"refs/heads/" + revision, "refs/tags/" + revision}
}

// FetchURL resolves the fetch value of a remote against the manifest project URL, as repo does with urljoin.
func FetchURL(fetch, manifestURL string) (string, error) {
	fetchURL, err := url.Parse(fetch)
	if err != nil {
		return "", err
	}
	if fetchURL.IsAbs() {
		return fetch, nil
	}
	base, err := url.Parse(manifestURL)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(fetchURL).String(), nil
}

// Revisions resolves each request under the host of its fetch URL and returns an *UnresolvedError naming the misses.
func Revisions(s RefSearcher, reqs []Request) ([]Result, error) {
	type search struct {
		host string
		ref  string
	}
	// One search per candidate ref and host returns that ref for every project on
	// the host, so the cost does not grow with the number of projects.
	found := make(map[search]map[string]entities.Hash)
	for _, req := range reqs {
		if entities.IsHash(req.Revision) || req.Revision == "" {
			continue
		}
		host := instance.Host(req.FetchURL)
		for _, ref := range Candidates(req.Revision) {
			key := search{host, ref}
			if _, ok := found[key]; ok {
				continue
			}
			hits, err := s.SearchRefs(host, "", ref, 0)
			if err != nil && !entities.IsNotExist(err) {
				return nil, fmt.Errorf("failed to search %s on %s: %w", ref, host, err)
			}
			byProject := make(map[string]entities.Hash, len(hits))
			for _, hit := range hits {
				// A search returns the refs below a prefix when the ref itself is missing.
				if hit.Ref == ref && !hit.Hash.IsZero() {
					byProject[hit.Project] = hit.Hash
				}
			}
			found[key] = byProject
		}
	}

	results := make([]Result, len(reqs))
	var unresolved []Request
	for i, req := range reqs {
		if entities.IsHash(req.Revision) {
			results[i] = Result{Hash: entities.NewHash(req.Revision)}
			continue
		}
		host := instance.Host(req.FetchURL)
		for _, ref := range Candidates(req.Revision) {
			if h, ok := found[search{host, ref}][req.Project]; ok {
				results[i] = Result{Ref: ref, Hash: h}
				break
			}
		}
		if results[i].Hash.IsZero() {
			unresolved = append(unresolved, req)
		}
	}
	if len(unresolved) > 0 {
		return results, &UnresolvedError{Requests: unresolved}
	}
	return results, nil
}

// MatchGroups reports whether a group filter selects a project, as repo matches them: in order, last match wins.
func MatchGroups(projectGroups, filter []string) bool {
	groups := append([]string{"all"}, projectGroups...)
	if !slices.Contains(groups, "notdefault") {
		groups = append(groups, "default")
	}
	if len(filter) == 0 {
		filter = []string{"default"}
	}
	match := false
	for _, g := range filter {
		if g == "" {
			continue
		}
		if name, ok := strings.CutPrefix(g, "-"); ok {
			if slices.Contains(groups, name) {
				match = false
			}
		} else if slices.Contains(groups, g) {
			match = true
		}
	}
	return match
}
