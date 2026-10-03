// Copyright 2016 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package manifest

import (
	"container/list"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

// MarshalXML serializes the receiver to XML respecting
// the inter-element order.
func (m *Manifest) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if err := m.Validate(); err != nil {
		return err
	}

	// Note that this also updates the pointer values held by Ordered
	for i := range m.Project {
		m.Project[i].prepare()
	}

	start.Name.Local = "manifest"
	if err := e.EncodeToken(start); err != nil {
		return err
	}

	for _, element := range m.Ordered {
		if err := e.Encode(element); err != nil {
			return err
		}
	}

	return e.EncodeToken(start.End())
}

// Maybe use reflection here in the future to account for newly added fields.
func (m *Manifest) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		if se, ok := token.(xml.StartElement); ok {
			switch se.Name.Local {
			case "notice":
				var n Notice
				if err := d.DecodeElement(&n, &se); err != nil {
					return err
				}
				if err := m.AddNotice(&n); err != nil {
					return err
				}
			case "remote":
				var r Remote
				if err := d.DecodeElement(&r, &se); err != nil {
					return err
				}
				m.AddRemote(&r)
			case "default":
				var def Default
				if err := d.DecodeElement(&def, &se); err != nil {
					return err
				}
				if err := m.AddDefault(&def); err != nil {
					return err
				}
			case "manifest-server":
				var ms ManifestServer
				if err := d.DecodeElement(&ms, &se); err != nil {
					return err
				}
				if err := m.AddManifestServer(&ms); err != nil {
					return err
				}
			case "remove-project":
				var rp RemoveProject
				if err := d.DecodeElement(&rp, &se); err != nil {
					return err
				}
				m.AddRemoveProject(&rp)
			case "project":
				var p Project
				if err := d.DecodeElement(&p, &se); err != nil {
					return err
				}
				m.AddProject(&p)
			case "extend-project":
				var ep ExtendProject
				if err := d.DecodeElement(&ep, &se); err != nil {
					return err
				}
				m.AddExtendProject(&ep)
			case "repo-hooks":
				var r RepoHooks
				if err := d.DecodeElement(&r, &se); err != nil {
					return err
				}
				if err := m.AddRepoHooks(&r); err != nil {
					return err
				}
			case "include":
				var i Include
				if err := d.DecodeElement(&i, &se); err != nil {
					return err
				}
				m.AddInclude(&i)
			case "superproject":
				var s SuperProject
				if err := d.DecodeElement(&s, &se); err != nil {
					return err
				}
				if err := m.AddSuperProject(&s); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (p *Project) prepare() {
	p.GroupsString = strings.Join(p.Groups, ",")
}

func (mf *Manifest) ProjectRevision(p *Project) string {
	if p.Revision != "" {
		return p.Revision
	}

	if mf.Default == nil {
		return ""
	}

	return mf.Default.Revision
}

func (mf *Manifest) ProjectRemote(p *Project) string {
	if p.Remote != "" {
		return p.Remote
	}

	if mf.Default == nil {
		return ""
	}

	return mf.Default.Remote
}

var notDefaultRe = regexp.MustCompile(`^notdefault$`)

// Filter removes all notdefault projects from a manifest.
func (mf *Manifest) Filter() {
	filtered := *mf
	filtered.Project = nil
	filtered.RemoveProject = nil
	filtered.ExtendProject = nil
	filtered.Ordered = nil
	filtered.OrderedXProject = nil

	// We need to update Ordered first to ensure variants hold for a valid manifest file:
	// RemoveProject and ExtendProject cannot refer to non-existing Projects
	// in the manifest in a top-down scan order.
	filter := make(map[string]bool)
	for _, p := range mf.Ordered {
		if p, ok := p.(*Project); ok {
			if p.GroupsMatch(notDefaultRe) {
				filter[p.Name] = true
				continue
			}
			// If another occurrence of the same Project.Name shows up,
			// with allowed groups, we erase any filtering to allow later Remove
			// and Extends
			filter[p.Name] = false
		}
		if p, ok := p.(*RemoveProject); ok {
			if filter[p.Name] {
				continue
			}
		}
		if p, ok := p.(*ExtendProject); ok {
			if filter[p.Name] {
				continue
			}
		}
		filtered.Ordered = append(filtered.Ordered, p)
	}

	// Now that Ordered is consistent, build other fields from this source of truth
	for _, ele := range filtered.Ordered {
		switch ele := ele.(type) {
		case *Project:
			filtered.Project = append(filtered.Project, ele)
			filtered.OrderedXProject = append(filtered.OrderedXProject, ele)
		case *RemoveProject:
			filtered.RemoveProject = append(filtered.RemoveProject, ele)
			filtered.OrderedXProject = append(filtered.OrderedXProject, ele)
		case *ExtendProject:
			filtered.ExtendProject = append(filtered.ExtendProject, ele)
			filtered.OrderedXProject = append(filtered.OrderedXProject, ele)
		default:
			// Do nothing
		}
	}

	*mf = filtered
}

type ManifestBytes func(ctx context.Context, name string) ([]byte, error)

func (mf *Manifest) IncludeManifestFile(ctx context.Context, file string, manifestBytes ManifestBytes) error {
	manBytes, err := manifestBytes(ctx, file)
	if err != nil {
		return err
	}
	included, err := Parse(manBytes)
	if err != nil {
		return err
	}

	return mf.MergeManifest(ctx, included, manifestBytes)
}

// FlattenIncludes unrolls all the includes specified in this manifest to produce a flattened manifest.
func (mf *Manifest) FlattenIncludes(ctx context.Context, manifestBytes ManifestBytes) error {
	if len(mf.Include) == 0 {
		return nil
	}

	newMan := &Manifest{}
	if err := newMan.MergeManifest(ctx, mf, manifestBytes); err != nil {
		return err
	}
	*mf = *newMan

	return nil
}

func (mf *Manifest) Validate() error {
	expectedOrderedCnt := 0
	expectedOrderedXProjectCnt := 0

	if mf.Notice != nil {
		expectedOrderedCnt++
	}
	expectedOrderedCnt += len(mf.Remote)
	if mf.Default != nil {
		expectedOrderedCnt++
	}
	if mf.ManifestServer != nil {
		expectedOrderedCnt++
	}
	expectedOrderedCnt += len(mf.RemoveProject)
	expectedOrderedXProjectCnt += len(mf.RemoveProject)
	expectedOrderedCnt += len(mf.Project)
	expectedOrderedXProjectCnt += len(mf.Project)
	expectedOrderedCnt += len(mf.ExtendProject)
	expectedOrderedXProjectCnt += len(mf.ExtendProject)
	if mf.RepoHooks != nil {
		expectedOrderedCnt++
	}
	expectedOrderedCnt += len(mf.Include)
	if mf.SuperProject != nil {
		expectedOrderedCnt++
	}

	if expectedOrderedCnt != len(mf.Ordered) {
		return fmt.Errorf("field Ordered contains %d elements but expected %d", len(mf.Ordered), expectedOrderedCnt)
	}

	if expectedOrderedXProjectCnt != len(mf.OrderedXProject) {
		return fmt.Errorf("field OrderedXProject contains %d elements but expected %d", len(mf.OrderedXProject), expectedOrderedXProjectCnt)
	}

	return nil
}

// MergeManifest merges the input manifest into this manifest to produce a flattened manifest
// with all the Includes unrolled.
func (mf *Manifest) MergeManifest(ctx context.Context, toMerge *Manifest, manifestBytes ManifestBytes) error {
	// We have to merge the elements in the correct order
	for _, ele := range toMerge.Ordered {
		switch ele := ele.(type) {
		case *Notice:
			if ele != nil {
				if err := mf.AddNotice(ele); err != nil {
					return err
				}
			}
		case *Remote:
			mf.AddRemote(ele)
		case *Default:
			if ele != nil {
				if err := mf.AddDefault(ele); err != nil {
					return err
				}
			}
		case *ManifestServer:
			if ele != nil {
				if err := mf.AddManifestServer(ele); err != nil {
					return err
				}
			}
		case *RemoveProject:
			mf.AddRemoveProject(ele)
		case *Project:
			mf.AddProject(ele)
		case *ExtendProject:
			mf.AddExtendProject(ele)
		case *RepoHooks:
			if ele != nil {
				if err := mf.AddRepoHooks(ele); err != nil {
					return err
				}
			}
		case *Include:
			if err := mf.IncludeManifestFile(ctx, ele.Name, manifestBytes); err != nil {
				return err
			}
		case *SuperProject:
			if ele != nil {
				if err := mf.AddSuperProject(ele); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("encountered unexpected type: %T", ele)
		}
	}

	return nil
}

// ApplyRemoves returns a new Project list with the resolved set of
// projects after applying remove-projects. The original manifest
// is left intact.
func (m *Manifest) ApplyRemoves() ([]*Project, error) {
	l := list.New()
	projects := make(map[string][]*list.Element)

	for _, ele := range m.OrderedXProject {
		switch ele := ele.(type) {
		case *Project:
			e := l.PushBack(ele)
			projects[ele.Name] = append(projects[ele.Name], e)
		case *RemoveProject:
			targets, found := projects[ele.Name]
			if !found {
				return nil, fmt.Errorf("encountered remove project without accompanying project for %q", ele.Name)
			}
			for _, tgt := range targets {
				l.Remove(tgt)
			}
			delete(projects, ele.Name)
		case *ExtendProject:
			// Nothing yet
		default:
			return nil, fmt.Errorf("encountered unexpected type: %T", ele)

		}
	}

	applied := make([]*Project, 0, l.Len())
	for e := l.Front(); e != nil; e = e.Next() {
		applied = append(applied, e.Value.(*Project))
	}

	return applied, nil
}

func CloneManifest(man *Manifest) (*Manifest, error) {
	buf, err := xml.MarshalIndent(man, "", " ")
	if err != nil {
		return nil, fmt.Errorf("cannot marshal manifest %w", err)
	}
	pinned, err := Parse(buf)
	if err != nil {
		return nil, fmt.Errorf("cannot unmarshal manifest %w", err)
	}
	return pinned, nil
}

func (m *Manifest) AddNotice(notice *Notice) error {
	if m.Notice != nil {
		return errors.New("notice element already exists")
	}

	m.Notice = notice
	m.Ordered = append(m.Ordered, notice)
	return nil
}

func (m *Manifest) AddRemote(remote *Remote) {
	m.Remote = append(m.Remote, remote)
	m.Ordered = append(m.Ordered, remote)
	if m.remotes == nil {
		m.remotes = make(map[string]*Remote)
	}
	m.remotes[remote.Name] = remote
}

func (m *Manifest) AddDefault(def *Default) error {
	if m.Default != nil {
		return errors.New("default element already exists")
	}

	m.Default = def
	m.Ordered = append(m.Ordered, def)
	return nil
}

func (m *Manifest) AddManifestServer(manifestServer *ManifestServer) error {
	if m.ManifestServer != nil {
		return errors.New("manifest-server element already exists")
	}

	m.ManifestServer = manifestServer
	m.Ordered = append(m.Ordered, manifestServer)
	return nil
}

func (m *Manifest) AddRemoveProject(removeProject *RemoveProject) {
	m.RemoveProject = append(m.RemoveProject, removeProject)
	m.OrderedXProject = append(m.OrderedXProject, removeProject)
	m.Ordered = append(m.Ordered, removeProject)
}

func (m *Manifest) AddProject(project *Project) {
	m.Project = append(m.Project, project)
	m.OrderedXProject = append(m.OrderedXProject, project)
	m.Ordered = append(m.Ordered, project)
}

func (m *Manifest) AddExtendProject(extendProject *ExtendProject) {
	m.ExtendProject = append(m.ExtendProject, extendProject)
	m.OrderedXProject = append(m.OrderedXProject, extendProject)
	m.Ordered = append(m.Ordered, extendProject)
}

func (m *Manifest) AddRepoHooks(repoHooks *RepoHooks) error {
	if m.RepoHooks != nil {
		return errors.New("repo-hooks element already exists")
	}

	m.RepoHooks = repoHooks
	m.Ordered = append(m.Ordered, repoHooks)
	return nil
}

func (m *Manifest) AddInclude(include *Include) {
	m.Include = append(m.Include, include)
	m.Ordered = append(m.Ordered, include)
}

func (m *Manifest) AddSuperProject(superProject *SuperProject) error {
	if m.SuperProject != nil {
		return errors.New("superproject element already exists")
	}

	m.SuperProject = superProject
	m.Ordered = append(m.Ordered, superProject)
	return nil
}

// RemoteFromName will query the internal name->Remote cache.
// It will only discover remotes that were added with the AddRemote
// method.
func (mf *Manifest) RemoteFromName(remote string) *Remote {
	if mf.remotes == nil {
		return nil
	}
	return mf.remotes[remote]
}

// ExpandFetch handles the special case value of ".."
func ExpandFetch(fetchURL, manifestProjectURL string) (string, error) {
	if !strings.HasPrefix(fetchURL, "..") {
		return fetchURL, nil
	}

	if manifestProjectURL == "" {
		return "", errors.New("cannot resolve '..' without a manifest project URL")
	}

	parsedFetchURL, err := url.Parse(fetchURL)
	if err != nil {
		return "", err
	}
	parsedManifestProjectURL, err := url.Parse(manifestProjectURL)
	if err != nil {
		return "", err
	}

	// This matches the Python urljoin behavior that the repo tool uses.
	return parsedManifestProjectURL.ResolveReference(parsedFetchURL).String(), nil
}
