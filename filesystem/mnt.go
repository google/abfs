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

package filesystem

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/abfs/manifest"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	rbemodel "github.com/google/abfs/shared/rbe/model"
)

type ProjectInfo struct {
	Commit       *object.Commit
	Git          *casgit.CASGit
	Project      *manifest.Project
	Server       string
	Manifest     *manifest.Manifest
	Packs        *binding.PackFiles
	PartialClone bool

	// Needed to resolve "fetch" URLs.
	SchemeManProjURL string
}

type MatchError struct {
	message string
}

func NewMatchError(format string, args ...any) *MatchError {
	return &MatchError{fmt.Sprintf("match failed: "+format, args...)}
}

func (e *MatchError) Error() string {
	return e.message
}
func IsMatchError(err error) bool {
	var matchErr *MatchError
	return errors.As(err, &matchErr)
}

type RBETapDigestInput struct {
	Reads  map[string]bindcas.CASBackendStat `json:"reads"`
	Writes map[string]bindcas.CASBackendStat `json:"writes"`
}

type RBEPathStat struct {
	Path string `json:"path"`
	rbemodel.Digest
	Mode uint32 `json:"mode,omitempty"`
}

type RBETapDigestResponse struct {
	Reads  []RBEPathStat `json:"reads"`
	Writes []RBEPathStat `json:"writes"`
}

type HTTPMount interface {
	// Hashes find the hash of a list of paths. Unknown paths will return the empty
	// file hash instead of an error.
	Hashes(ctx context.Context, goCnt int64, prefix string, paths []string) (result []string, errs map[string]string)
	// HashStats find the hash, size, and file mode of a list of paths. Unknown paths will return the empty
	// file hash instead of an error.
	HashStats(ctx context.Context, goCnt int64, prefix string, paths []string) (result []entities.HashStat, errs map[string]string)
	Matches(ctx context.Context, goCnt int64, path string, patch entities.Patch) error
	MatchAndPatch(ctx context.Context, goCnt int64, path string, readPatch, writePatch entities.Patch, deletes entities.DeletePatch) error
	NewGitDir(ctx context.Context, root, dir bindcas.Dir, info ProjectInfo) error
	NewRepoObjDir(ctx context.Context, root, dir bindcas.Dir, name string, info ProjectInfo) error
	Root() bindcas.Dir
	SetRBEDigest(ctx context.Context, path string, digest rbemodel.Digest, mode uint32) error
	GetRBEDigest(ctx context.Context, path string) (*RBEPathStat, error)
	SetRBEDigests(ctx context.Context, goCount int64, digests []RBEPathStat) (errs map[string]string)
	GetRBEDigests(ctx context.Context, goCount int64, paths []string) (result []RBEPathStat, errs map[string]string)
	GetRBETapDigest(ctx context.Context, goCount int64, input RBETapDigestInput) (*RBETapDigestResponse, error)
	LoadTreeDigestMap(ctx context.Context, tree entities.Hash) error
	AddDigestMapEntries(entries []binding.DigestEntry)
}

type MountInfo struct {
	CacheDir       string
	LocalOnly      bool
	FetchTimeout   time.Duration
	PartialClone   bool
	RemoteReadOnly bool
}

type Mount interface {
	HTTPMount

	SetInitConfig(c *entities.FSConfig)
	Config() *entities.FSConfig
	Mount(ctx context.Context, mountPoint string, mntInfo MountInfo, store entities.CASStore) error
	Unmount()
	Chroot(ctx context.Context, path string) (bindcas.Dir, error)
	Serve() error
	Ready()
	CASFS() bindcas.CASFS
	CreateDir(path string) bindcas.Dir
}

const (
	configTemplate = `[core]
	compression = 0
	looseCompression = 0
	abbrev = 40
[fetch]
	unpackLimit = 2147483647
[gc]
	auto = 0
	packRefs = false
[maintenance "loose-objects"]
	auto = 0
[maintenance "incremental-repack"]
	auto = 0
`

	casfsTemplate = `[casfs]
	noObjectCompress = false
	flatObjectPath = false
	disableQuickLookup = true
`
)

func ProjectGitConfig(info ProjectInfo) ([]byte, error) {
	if info.Manifest == nil {
		return []byte(configTemplate + casfsTemplate), nil
	}

	if info.Project == nil {
		return []byte(configTemplate + casfsTemplate), nil
	}

	if len(info.Manifest.Remote) == 0 {
		return []byte(configTemplate + casfsTemplate), nil
	}

	projectRemote := info.Manifest.ProjectRemote(info.Project)
	if projectRemote == "" {
		return []byte(configTemplate + casfsTemplate), nil
	}
	remote := info.Manifest.RemoteFromName(projectRemote)
	if remote == nil {
		return []byte(configTemplate + casfsTemplate), nil
	}

	fetch, err := manifest.ExpandFetch(remote.Fetch, info.SchemeManProjURL)
	if err != nil {
		return nil, errors.New("could not expand fetch URL")
	}
	remoteTemplate := new(strings.Builder)
	fmt.Fprintf(remoteTemplate, "[remote \"%s\"]\n", projectRemote)
	fmt.Fprintf(remoteTemplate, "	fetch = +refs/heads/*:refs/remotes/%s/*\n", projectRemote)
	fmt.Fprintf(remoteTemplate, "	url = %s/%s\n", strings.Trim(fetch, "/"), info.Project.Name)
	if remote.Review != "" {
		fmt.Fprintf(remoteTemplate, "	review = %s\n", remote.Review)
	}
	fmt.Fprintf(remoteTemplate, "	projectname = %s\n", info.Project.Name)
	if info.PartialClone {
		fmt.Fprint(remoteTemplate, "	promisor = true\n")
		fmt.Fprint(remoteTemplate, "	partialclonefilter = tree:0\n")
	}
	return []byte(configTemplate + remoteTemplate.String() + casfsTemplate), nil
}
