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

package manifest

import (
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Effective applies the remove-project and extend-project elements of a flattened manifest
// in document order, with the rules of the repo tool, and drops them, so that Project holds
// the projects repo checks out.
func (m *Manifest) Effective() error {
	if len(m.Include) > 0 {
		return errors.New("cannot apply the projects of a manifest with includes; flatten it first")
	}

	// A name stays known after its paths are removed one by one, as in repo.
	byName := make(map[string][]*Project)
	byPath := make(map[string]*Project)
	removed := make(map[*Project]bool)
	var mismatches []string
	for _, ele := range m.Ordered {
		var (
			found []string
			err   error
		)
		switch ele := ele.(type) {
		case *Project:
			path := projectPath(ele)
			if _, ok := byPath[path]; ok {
				return fmt.Errorf("duplicate path %s", path)
			}
			byPath[path] = ele
			byName[ele.Name] = append(byName[ele.Name], ele)
		case *ExtendProject:
			found, err = m.applyExtend(ele, byName, byPath)
		case *RemoveProject:
			found, err = m.applyRemove(ele, byName, byPath, removed)
		}
		if err != nil {
			return err
		}
		mismatches = append(mismatches, found...)
	}
	if len(mismatches) > 0 {
		return fmt.Errorf("detected base-revision mismatch, updates needed:\n%s", strings.Join(mismatches, "\n"))
	}

	ordered := make([]any, 0, len(m.Ordered))
	m.Project = nil
	m.RemoveProject = nil
	m.ExtendProject = nil
	m.OrderedXProject = nil
	for _, ele := range m.Ordered {
		switch ele := ele.(type) {
		case *RemoveProject, *ExtendProject:
			continue
		case *Project:
			if removed[ele] {
				continue
			}
			m.Project = append(m.Project, ele)
			m.OrderedXProject = append(m.OrderedXProject, ele)
		}
		ordered = append(ordered, ele)
	}
	m.Ordered = ordered
	return nil
}

func (m *Manifest) applyExtend(e *ExtendProject, byName map[string][]*Project, byPath map[string]*Project) ([]string, error) {
	projects, ok := byName[e.Name]
	if !ok {
		return nil, fmt.Errorf("extend-project element specifies non-existent project: %s", e.Name)
	}
	if e.Remote != "" && m.RemoteFromName(e.Remote) == nil {
		return nil, fmt.Errorf("remote %s not defined for extend-project %s", e.Remote, e.Name)
	}
	if e.DestPath != "" && e.Path == "" && len(projects) > 1 {
		return nil, fmt.Errorf("extend-project cannot use dest-path when matching multiple projects: %s", e.Name)
	}

	var mismatches []string
	for _, p := range projects {
		if e.Path != "" && projectPath(p) != e.Path {
			continue
		}
		if len(e.Groups) > 0 {
			p.Groups = addGroups(p.Groups, e.Groups)
			p.GroupsString = strings.Join(p.Groups, ",")
		}
		if e.Revision != "" {
			if rev := m.effectiveRevision(p); e.BaseRev != "" && rev != e.BaseRev {
				mismatches = append(mismatches, fmt.Sprintf("extend-project name %s:\n  base %s vs revision %s", e.Name, e.BaseRev, rev))
			}
			p.Revision = e.Revision
		}
		if e.Remote != "" {
			// repo keeps the revision the project has; it does not take the new remote's.
			p.Revision = m.effectiveRevision(p)
			p.Remote = e.Remote
		}
		if e.DestBranch != "" {
			p.DestBranch = e.DestBranch
		}
		if e.Upstream != "" {
			p.Upstream = e.Upstream
		}
		if e.DestPath != "" {
			dest := strings.TrimRight(e.DestPath, "/")
			if other, ok := byPath[dest]; ok && other != p {
				return nil, fmt.Errorf("extend-project %s cannot move %s to %s: the path is used by %s", e.Name, projectPath(p), dest, other.Name)
			}
			delete(byPath, projectPath(p))
			p.Path = dest
			byPath[dest] = p
		}
		p.Copyfile = append(p.Copyfile, e.Copyfile...)
		p.Linkfile = append(p.Linkfile, e.Linkfile...)
		p.Annotation = append(p.Annotation, e.Annotation...)
	}
	return mismatches, nil
}

func (m *Manifest) applyRemove(r *RemoveProject, byName map[string][]*Project, byPath map[string]*Project, removed map[*Project]bool) ([]string, error) {
	if r.Name == "" && r.Path == "" {
		return nil, errors.New("remove-project must have name and/or path")
	}

	var mismatches []string
	checkBase := func(what string, p *Project) {
		if rev := m.effectiveRevision(p); r.BaseRev != "" && rev != r.BaseRev {
			mismatches = append(mismatches, fmt.Sprintf("remove-project %s:\n  base %s vs revision %s", what, r.BaseRev, rev))
		}
	}
	found := false
	if r.Path == "" {
		for _, p := range byName[r.Name] {
			checkBase("name "+r.Name, p)
			delete(byPath, projectPath(p))
			removed[p] = true
			found = true
		}
		if found {
			delete(byName, r.Name)
		}
	} else if p, ok := byPath[r.Path]; ok && (r.Name == "" || r.Name == p.Name) {
		checkBase("path "+r.Path, p)
		byName[p.Name] = slices.DeleteFunc(byName[p.Name], func(q *Project) bool { return q == p })
		delete(byPath, r.Path)
		removed[p] = true
		found = true
	}
	if !found && !xmlBool(r.Optional) {
		return nil, fmt.Errorf("remove-project element specifies non-existent project: %s", elementString(r))
	}
	return mismatches, nil
}

// effectiveRevision returns the revision repo gives a project before it is resolved:
// its own, else its remote's, else the default's.
func (m *Manifest) effectiveRevision(p *Project) string {
	if p.Revision != "" {
		return p.Revision
	}
	remote := p.Remote
	if remote == "" && m.Default != nil {
		remote = m.Default.Remote
	}
	if r := m.RemoteFromName(remote); r != nil && r.Revision != "" {
		return r.Revision
	}
	if m.Default != nil {
		return m.Default.Revision
	}
	return ""
}

// projectPath returns the path repo uses to tell projects apart.
func projectPath(p *Project) string {
	return strings.TrimRight(p.GetPath(), "/")
}

func xmlBool(s string) bool {
	switch s {
	case "true", "yes", "1":
		return true
	}
	return false
}

// elementString writes an element as repo prints it in errors, empty elements self-closed.
func elementString(ele any) string {
	b, err := xml.Marshal(ele)
	if err != nil {
		return fmt.Sprintf("%+v", ele)
	}
	s := string(b)
	if start := strings.Index(s, ">"); start > 0 && strings.HasPrefix(s, "<") {
		if end := "</" + strings.Fields(s[1:start])[0] + ">"; s[start+1:] == end {
			return s[:start] + "/>"
		}
	}
	return s
}
