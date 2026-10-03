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
	"bufio"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/abfs/manifest"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/casgit"
	"github.com/google/abfs/git"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

var ErrNotRepo = errors.New("not a repo")

const (
	ProjectsListFile = "projects.list"
	CopyLinkFile     = "copy-link-files.json"
)

type RepoOpts struct {
	Store            entities.CASStore
	Git              *casgit.CASGit
	CASFS            bindcas.CASFS
	Client           *binding.Client
	PrefetchIndices  bool
	PrefetchSymlinks bool
}

type Repo struct {
	RepoOpts
	shellGit     *git.ShellGit
	repoRoot     string
	dotRepo      string
	manifestsDir string

	flatMan        *manifest.Manifest
	groupsInclude  map[string]struct{}
	groupsExclude  map[string]struct{}
	projectsByName map[string]*manifest.Project
	projectsByPath map[string]*manifest.Project

	manifestURL     string
	manifestRemote  string
	manifestBranch  string
	manifestProject *casgit.GitRepo

	cwd          string
	curProjects  map[string]struct{}
	curLinkFiles map[string]struct{}
	curCopyFiles map[string]struct{}
}

func NewRepo(opts RepoOpts, repoPath string) (*Repo, error) {
	repoRoot, err := FindRepoRoot(repoPath)
	if err != nil {
		return nil, err
	}
	r := NewRepoForceRoot(opts, repoRoot)
	r.cwd = repoPath
	return r, nil
}

func NewRepoForceRoot(opts RepoOpts, repoRoot string) *Repo {
	manifestsDir := path.Join(repoRoot, ".repo", "manifests")
	return &Repo{
		RepoOpts:        opts,
		shellGit:        git.NewShellGit(""),
		repoRoot:        repoRoot,
		dotRepo:         path.Join(repoRoot, ".repo"),
		manifestsDir:    manifestsDir,
		manifestProject: casgit.NewGitRepo(opts.Git, manifestsDir, manifestsDir+".git", path.Join(manifestsDir+".git", "objects")),
	}
}

func (r *Repo) Root() string {
	return r.repoRoot
}

func (r *Repo) RelativeToRoot(p string) (string, error) {
	if r.cwd == "" {
		return "", fmt.Errorf("cwd not set")
	}
	absPath := p
	if !filepath.IsAbs(p) {
		absPath = filepath.Join(r.cwd, p)
	}
	absPath = filepath.Clean(absPath)
	relPath, err := filepath.Rel(r.repoRoot, absPath)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(relPath, "../") || relPath == ".." {
		return "", fmt.Errorf("path %s is outside repo root", absPath)
	}
	return relPath, nil
}

func (r *Repo) Load(ctx context.Context) error {
	if err := r.manifestProject.Load(); err != nil {
		return err
	}

	if err := r.loadManifest(ctx); err != nil {
		return err
	}

	if err := r.loadConfig(); err != nil {
		return err
	}

	r.filterProjectsByGroups()

	if err := r.readProjectsListFile(); err != nil {
		return err
	}
	if err := r.readCopyLinkFiles(); err != nil {
		return err
	}
	return nil
}

func (r *Repo) filterProjectsByGroups() {
	byName := make(map[string]*manifest.Project, len(r.flatMan.Project))
	byPath := make(map[string]*manifest.Project, len(r.flatMan.Project))
	for _, p := range r.flatMan.Project {
		if !GroupsMatch(p.Groups, r.groupsInclude, r.groupsExclude) {
			continue
		}
		byName[p.Name] = p
		byPath[p.GetPath()] = p
	}
	r.projectsByName = byName
	r.projectsByPath = byPath
}

func (r *Repo) loadManifest(ctx context.Context) error {
	manifestPath := path.Join(r.dotRepo, "manifest.xml")
	overridePath := path.Join(r.manifestsDir, "smart_sync_override.xml")
	if _, err := os.Stat(overridePath); err == nil {
		manifestPath = overridePath
	} else if !entities.IsNotExist(err) {
		return err
	}

	masterManBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var m *manifest.Manifest
	if err := xml.Unmarshal(masterManBytes, &m); err != nil {
		return err
	}

	if err := m.FlattenIncludes(ctx, func(ctx context.Context, name string) ([]byte, error) {
		if !filepath.IsAbs(name) {
			name = filepath.Join(r.dotRepo, "manifests", name)
		}
		return os.ReadFile(name)
	}); err != nil {
		return err
	}

	if err := m.Validate(); err != nil {
		return err
	}

	// TODO maybe move this to manfiest parser
	for _, p := range m.Project {
		if p.GroupsString != "" {
			p.Groups = strings.Split(p.GroupsString, ",")
		} else {
			p.Groups = nil
		}
	}

	r.flatMan = m
	return nil
}

func FindRepoRoot(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		dir = abs
	}
	for {
		_, err := os.Stat(path.Join(dir, ".repo", "repo") + "/")
		if err == nil {
			return dir, nil
		}
		if !entities.IsNotExist(err) {
			return "", err
		}
		if dir == "/" {
			break
		}
		dir = filepath.Dir(dir)
	}
	return "", ErrNotRepo
}

func (r *Repo) WriteFlattenManifest() error {
	data, err := xml.MarshalIndent(r.flatMan, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path.Join(r.dotRepo, ".flat-manifest.xml"), data, 0644)
}

type RepoCopyLinkFiles struct {
	Link []string `json:"linkfile"`
	Copy []string `json:"copyfile"`
}

func (r *Repo) readCopyLinkFiles() (retErr error) {
	file, err := os.Open(path.Join(r.dotRepo, CopyLinkFile))
	if entities.IsNotExist(err) {
		r.curCopyFiles = make(map[string]struct{})
		r.curLinkFiles = make(map[string]struct{})
		return nil
	}
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(file, &retErr)

	var v RepoCopyLinkFiles
	if err := json.NewDecoder(file).Decode(&v); err != nil {
		return err
	}

	r.curLinkFiles = make(map[string]struct{})
	for _, linkFile := range v.Link {
		r.curLinkFiles[linkFile] = struct{}{}
	}
	r.curCopyFiles = make(map[string]struct{})
	for _, copyFile := range v.Copy {
		r.curCopyFiles[copyFile] = struct{}{}
	}
	return nil
}

func (r *Repo) writeCopyLinkFiles() (retErr error) {
	v := RepoCopyLinkFiles{
		Link: make([]string, 0, len(r.curLinkFiles)),
		Copy: make([]string, 0, len(r.curCopyFiles)),
	}
	for k := range r.curLinkFiles {
		v.Link = append(v.Link, k)
	}
	for k := range r.curCopyFiles {
		v.Copy = append(v.Copy, k)
	}

	file, err := os.Create(path.Join(r.dotRepo, CopyLinkFile))
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(file, &retErr)

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	return enc.Encode(&v)
}

func (r *Repo) readProjectsListFile() (retErr error) {
	file, err := os.Open(path.Join(r.dotRepo, ProjectsListFile))
	if entities.IsNotExist(err) {
		r.curProjects = make(map[string]struct{})
		return nil
	}
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(file, &retErr)

	projects := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		projects[line] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	r.curProjects = projects
	return nil
}

func (r *Repo) writeProjectsListFile() (retErr error) {
	file, err := os.Create(path.Join(r.dotRepo, ProjectsListFile))
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(file, &retErr)

	list := make([]string, 0, len(r.curProjects))
	for p := range r.curProjects {
		list = append(list, p)
	}
	sort.Strings(list)
	for _, p := range list {
		if _, err := file.WriteString(p + "\n"); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) writeRepoFiles() error {
	if err := r.writeProjectsListFile(); err != nil {
		return err
	}
	if err := r.writeCopyLinkFiles(); err != nil {
		return err
	}
	return nil
}

func (r *Repo) ManifestNotice() string {
	if r.flatMan == nil || r.flatMan.Notice == nil {
		return ""
	}
	return string(*r.flatMan.Notice)
}
