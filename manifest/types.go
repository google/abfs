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

// Package manifest manipulates Manifest files as described at
// https://gerrit.googlesource.com/git-repo/+/HEAD/docs/manifest-format.md
//
// This package started as the manifest package of Google's open source
// slothfs project (https://github.com/google/slothfs, Apache License 2.0)
// and has since been substantially extended for ABFS.
package manifest

import (
	"encoding/xml"
	"regexp"
)

// Copyfile indicates that a file should be copied in a checkout
type Copyfile struct {
	Src  string `xml:"src,attr" json:"src"`
	Dest string `xml:"dest,attr" json:"dest"`
}

// Linkfile indicates that a file should be symlinked in a checkout
type Linkfile struct {
	Src  string `xml:"src,attr" json:"src"`
	Dest string `xml:"dest,attr" json:"dest"`
}

type Annotation struct {
	Name  string `xml:"name,attr" json:"name"`
	Value string `xml:"value,attr" json:"value"`
	Keep  *bool  `xml:"keep,attr,omitempty" json:"keep,omitempty"` // default is `true`
}

type SuperProject struct {
	XMLName xml.Name `xml:"superproject"`
	Project
}

// Project represents a single git repository that should be stitched
// into the checkout.
type Project struct {
	XMLName      xml.Name `xml:"project"`
	Name         string   `xml:"name,attr" json:"name"`
	Path         string   `xml:"path,attr,omitempty" json:"path,omitempty"`
	Remote       string   `xml:"remote,attr,omitempty" json:"remote,omitempty"`
	Revision     string   `xml:"revision,attr,omitempty" json:"revision,omitempty"`
	DestBranch   string   `xml:"dest-branch,attr,omitempty" json:"destBranch,omitempty"`
	GroupsString string   `xml:"groups,attr,omitempty" json:"-"`
	Groups       []string `xml:"-" json:"groups,omitempty"`
	SyncC        string   `xml:"sync-c,attr,omitempty" json:"syncC,omitempty"`
	SyncS        string   `xml:"sync-s,attr,omitempty" json:"syncS,omitempty"`
	SyncTags     string   `xml:"sync-tags,attr,omitempty" json:"syncTags,omitempty"`
	Upstream     string   `xml:"upstream,attr,omitempty" json:"upstream,omitempty"`
	CloneDepth   string   `xml:"clone-depth,attr,omitempty" json:"cloneDepth,omitempty"`
	ForcePath    string   `xml:"force-path,attr,omitempty" json:"forcePath,omitempty"`

	Annotation []*Annotation `xml:"annotaion,omitempty" json:"annotaions,omitempty"`
	Copyfile   []*Copyfile   `xml:"copyfile,omitempty" json:"copyFiles,omitempty"`
	Linkfile   []*Linkfile   `xml:"linkfile,omitempty" json:"linkFiles,omitempty"`
}

// GetPath provides the path where to place the repository.
func (p *Project) GetPath() string {
	if p.Path != "" {
		return p.Path
	}
	return p.Name
}

// GroupsMatch returns true if one of the project groups matches the input regex
func (p *Project) GroupsMatch(regex *regexp.Regexp) bool {
	for _, g := range p.Groups {
		if regex.MatchString(g) {
			return true
		}
	}
	return false
}

// Remote describes a host where a set of projects is hosted.
type Remote struct {
	XMLName  xml.Name `xml:"remote"`
	Name     string   `xml:"name,attr" json:"name"`
	Alias    string   `xml:"alias,attr,omitempty" json:"alias,omitempty"`
	Fetch    string   `xml:"fetch,attr" json:"fetch"`
	PushURL  string   `xml:"pushurl,attr,omitempty" json:"pushurl,omitempty"`
	Review   string   `xml:"review,attr,omitempty" json:"review,omitempty"`
	Revision string   `xml:"revision,attr,omitempty" json:"revision,omitempty"`
}

// Default holds default Project settings.
type Default struct {
	XMLName    xml.Name `xml:"default"`
	Remote     string   `xml:"remote,attr,omitempty" json:"remote,omitempty"`
	Revision   string   `xml:"revision,attr,omitempty" json:"revision,omitempty"`
	DestBranch string   `xml:"dest-branch,attr,omitempty" json:"destBranch,omitempty"`
	Upstream   string   `xml:"upstream,attr,omitempty" json:"upstream,omitempty"`
	SyncJ      string   `xml:"sync-j,attr,omitempty" json:"syncJ,omitempty"`
	SyncC      string   `xml:"sync-c,attr,omitempty" json:"syncC,omitempty"`
	SyncS      string   `xml:"sync-s,attr,omitempty" json:"syncS,omitempty"`
	SyncTags   string   `xml:"sync-tags,attr,omitempty" json:"syncTags,omitempty"`
}

type ManifestServer struct {
	XMLName xml.Name `xml:"manifest-server"`
	URL     string   `xml:"url,attr" json:"url"`
	Helper  string   `xml:"helper,attr" json:"helper"`
}

type RemoveProject struct {
	XMLName xml.Name `xml:"remove-project"`
	Name    string   `xml:"name,attr" json:"name"`
}

type ExtendProject struct {
	XMLName      xml.Name `xml:"extend-project"`
	Name         string   `xml:"name,attr" json:"name"`
	Path         string   `xml:"path,attr,omitempty" json:"path,omitempty"`
	GroupsString string   `xml:"groups,attr,omitempty" json:"-"`
	Groups       []string `xml:"-" json:"groups,omitempty"`
	Revision     string   `xml:"revision,attr,omitempty" json:"revision,omitempty"`
	Remote       string   `xml:"remote,attr,omitempty" json:"remote,omitempty"`
}

type RepoHooks struct {
	XMLName     xml.Name `xml:"repo-hooks"`
	InProject   string   `xml:"in-project,attr" json:"inProject"`
	EnabledList string   `xml:"enabled-list,attr" json:"enabledList"`
}

type Include struct {
	XMLName xml.Name `xml:"include"`
	Name    string   `xml:"name,attr" json:"name"`
}

// Make a custom Notice type so that it can be marshalled
// standalone with proper xml.Name
type Notice string

func (n *Notice) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	start.Name = xml.Name{Local: "notice"}
	return e.EncodeElement(string(*n), start)
}

func (n *Notice) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var s string
	if err := d.DecodeElement(&s, &start); err != nil {
		return err
	}
	*n = Notice(s)
	return nil
}

func (n *Notice) String() string {
	return string(*n)
}

// Manifest holds the entire manifest, describing a set of git
// projects to be stitched together.
// Adding to slice based elements should only be
// done through exported methods.
// Setting pointer based elements should only be
// done through exported methods.
// Edits in place to already existing data is safe.
// Note that there aren't exported methods for deletion because
// traditional manifest parsing does not require that.
type Manifest struct {
	XMLName        xml.Name         `xml:"manifest"`
	Notice         *Notice          `xml:"notice,omitempty" json:"notice,omitempty"`
	Remote         []*Remote        `xml:"remote,omitempty" json:"remotes,omitempty"`
	Default        *Default         `xml:"default,omitempty" json:"parsedDefault,omitempty"`
	ManifestServer *ManifestServer  `xml:"manifest-server,omitempty" json:"manifestServer,omitempty"`
	RemoveProject  []*RemoveProject `xml:"remove-project,omitempty" json:"removeProjects,omitempty"`
	Project        []*Project       `xml:"project" json:"projects"`
	ExtendProject  []*ExtendProject `xml:"extend-project,omitempty" json:"extendProjects,omitempty"`
	RepoHooks      *RepoHooks       `xml:"repo-hooks,omitempty" json:"repoHooks,omitempty"`
	Include        []*Include       `xml:"include,omitempty" json:"includes,omitempty"`
	SuperProject   *SuperProject    `xml:"superproject,omitempty" json:"superproject,omitempty"`

	// Order among all elements
	Ordered []any `xml:"-" json:"-"`

	// Order among Project, RemoveProject, and ExtendProject
	// Useful to determine whether a project should be removed from a manifest
	OrderedXProject []any `xml:"-" json:"-"`

	// Name to Remote cache.
	remotes map[string]*Remote
}
