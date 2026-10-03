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

package gitpusher

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/fastgit"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/remote"
)

const (
	minObjectsToPush = 100
	maxDelayToPush   = 7 * 24 * time.Hour // A week
)

type Git interface {
	AllObjects(repoPath string) ([]entities.Hash, error)
	RevListAll(repoPath string) ([]entities.Hash, error)
	PackObjects(repoPath string, prefix string, objects []entities.Hash) (string, error)
	AllCommitsDates(repoPath string) ([]entities.Hash, []time.Time, error)
	CommitTreeHashes(root string, commits []entities.Hash) ([]entities.Hash, error)
	CommitsFileObjects(root string, commits []entities.Hash) ([]entities.Hash, error)
	ObjectTypes(repoPath string, objects []entities.Hash) ([]string, error)
}

type PackPusher struct {
	server                  string
	project                 string
	gitRoot                 string
	git                     Git
	client                  remote.RemoteProvider
	packs                   *binding.PackFiles
	strategy                binding.PackingStrategy
	allObjectsForCompaction map[entities.Hash]struct{}
}

func NewPackPusher(client remote.RemoteProvider, server, project, gitRoot string, git Git, strategy binding.PackingStrategy) (*PackPusher, error) {
	p := &PackPusher{
		server:   server,
		project:  project,
		gitRoot:  gitRoot,
		git:      git,
		client:   client,
		strategy: strategy,
	}
	return p, p.init(context.Background())
}

func (p *PackPusher) packRoot() string {
	return path.Join(p.gitRoot, "pushed-packs.v4")
}

// cleanPackRoot all the files in the pack root that are not in the packs list.
func (p *PackPusher) cleanPackRoot(packs []binding.PackEntry) error {
	ents, err := os.ReadDir(p.packRoot())
	if err != nil {
		return err
	}
	for _, ent := range ents {
		found := false
		for _, pack := range packs {
			if ent.Name() == pack.PackID+".pack" || ent.Name() == pack.PackID+".idx" {
				found = true
				break
			}
		}
		if found {
			continue
		}
		p := path.Join(p.packRoot(), ent.Name())
		if err := os.RemoveAll(p); err != nil {
			return err
		}
		log.Printf("Removed %s", p)
	}
	return nil
}

func (p *PackPusher) createInitPacks(ctx context.Context) error {
	log.Printf("Initializing the git-packs for %s with strategy %q", p.project, p.strategy)

	if err := p.cleanPackRoot(nil); err != nil {
		return err
	}

	allCommits, allDates, err := p.git.AllCommitsDates(p.gitRoot)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	groups := make(map[time.Time][]entities.Hash)
	for i, h := range allCommits {
		g := timeToGroup(allDates[i], now)
		groups[g] = append(groups[g], h)
	}

	groupKeys := make([]time.Time, 0, len(groups))
	for k := range groups {
		groupKeys = append(groupKeys, k)
	}
	sort.Slice(groupKeys, func(i, j int) bool {
		return groupKeys[i].Before(groupKeys[j])
	})

	var packs []binding.PackEntry
	var pendings []entities.Hash
	for i, k := range groupKeys {
		commits := groups[k]
		if pendings == nil {
			pendings = commits
		} else {
			pendings = append(pendings, commits...)
		}
		if len(pendings) < minObjectsToPush && (len(pendings) == 0 || i < len(groupKeys)-1) {
			continue
		}
		newPacks, err := p.addPacks(ctx, pendings, k)
		if err != nil {
			return err
		}
		packs = append(packs, newPacks...)
		pendings = nil
	}

	p.packs = &binding.PackFiles{
		Version:        binding.CurrentPackFilesVersion,
		LastUpdateTime: now,
		Strategy:       p.strategy,
		Packs:          packs,
	}

	return p.putPacksObject(ctx)
}

func (p *PackPusher) addPacks(ctx context.Context, commits []entities.Hash, packTime time.Time) ([]binding.PackEntry, error) {
	var packs []binding.PackEntry

	pack, err := p.packObjects(ctx, commits, binding.CommitsPack, packTime)
	if err != nil {
		return nil, err
	}
	packs = append(packs, pack)

	if p.strategy == binding.PackingStrategyCommits {
		return packs, nil
	}

	commitTrees, err := p.git.CommitTreeHashes(p.gitRoot, commits)
	if err != nil {
		return nil, err
	}
	fileObjects, err := p.git.CommitsFileObjects(p.gitRoot, commitTrees)
	if err != nil {
		return nil, err
	}
	objTypes, err := p.git.ObjectTypes(p.gitRoot, fileObjects)
	if err != nil {
		return nil, err
	}
	var treeHashes []entities.Hash
	for i, objType := range objTypes {
		if objType == "tree" {
			treeHashes = append(treeHashes, fileObjects[i])
		}
	}
	pack, err = p.packObjects(ctx, treeHashes, binding.TreesPack, packTime)
	if err != nil {
		return nil, err
	}
	packs = append(packs, pack)

	if p.strategy == binding.PackingStrategyTrees {
		return packs, nil
	}

	var blobsHashes []entities.Hash
	for i, objType := range objTypes {
		if objType == "blob" {
			blobsHashes = append(blobsHashes, fileObjects[i])
		}
	}

	if p.strategy == binding.PackingStrategyBlobsList {
		pack, err = p.packObjects(ctx, blobsHashes, binding.BlobsListPack, packTime)
		if err != nil {
			return nil, err
		}
		packs = append(packs, pack)
	} else {
		pack, err = p.packObjects(ctx, blobsHashes, binding.BlobsPack, packTime)
		if err != nil {
			return nil, err
		}
		packs = append(packs, pack)
	}
	return packs, nil
}

func (p *PackPusher) putPacksObject(ctx context.Context) error {
	p.packs.LastUpdateTime = time.Now()
	if err := p.packs.Encode(); err != nil {
		return err
	}
	if err := p.client.PutObject(ctx, p.packs.Hash, remote.NewReaderFromBytes(p.packs.Encoded)); err != nil {
		return err
	}
	return p.client.PutRefs(ctx, p.server, p.project, []remote.RefHash{
		{
			Ref:  binding.PacksRefs,
			Hash: p.packs.Hash,
		},
	})
}

func (p *PackPusher) init(ctx context.Context) error {
	if p.strategy == binding.PackingStrategyNone {
		return nil
	}

	if err := os.MkdirAll(p.packRoot(), 0755); err != nil {
		return err
	}

	refs, err := p.client.GetRefs(ctx, p.server, p.project, []string{binding.PacksRefs})
	if err != nil {
		return err
	}

	packs, err := remote.FetchObject[binding.PackFiles](ctx, p.client, refs[0])
	var packEntries []binding.PackEntry
	if err != nil {
		if !entities.IsNotExist(err) {
			return err
		}
	} else {
		p.packs = packs
		packEntries = packs.Packs
	}

	if err := p.cleanPackRoot(packEntries); err != nil {
		return err
	}

	for _, pack := range packEntries {
		if err := p.download(path.Join(p.packRoot(), pack.PackID+".pack"), pack.Content, pack.ContentSize); err != nil {
			return err
		}
		if err := p.download(path.Join(p.packRoot(), pack.PackID+".idx"), pack.Index, pack.IndexSize); err != nil {
			return err
		}
	}

	return nil
}

func (p *PackPusher) needsDownload(path string, hash entities.Hash, size int64) (bool, error) {
	stat, err := os.Stat(path)
	if err != nil {
		if !entities.IsNotExist(err) {
			return false, err
		}
		return true, nil
	}

	if stat.Size() == size {
		// if size matches we assume it is same
		return false, nil
	}

	log.Printf("Expected existing file %q (hash %s) to have size of %d, got %d", path, hash, size, stat.Size())
	return true, nil
}

func (p *PackPusher) download(path string, hash entities.Hash, size int64) (retErr error) {
	if needsDownload, err := p.needsDownload(path, hash, size); err != nil {
		return err
	} else if !needsDownload {
		return nil
	}

	reader, err := p.client.GetObject(context.Background(), hash)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(reader, &retErr)

	n, err := objects.ReadBlobHeader(reader)
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("blob size for %s is %d which is different from expected %d (to write file %q)", hash, n, size, path)
	}

	f, err := os.OpenFile(path+".tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()

	n, err = io.Copy(f, reader)
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("expected to write file %q with size of %d, wrote %d", path, size, n)
	}

	if err := f.Close(); err != nil {
		return err
	}

	return os.Rename(path+".tmp", path)
}

func (p *PackPusher) addPackObjects(pack binding.PackEntry, objects map[entities.Hash]struct{}) error {
	idxPath := path.Join(p.packRoot(), pack.PackID+".idx")
	idx, err := fastgit.ParseIndexFile(idxPath)
	if err != nil {
		return err
	}
	for _, objs := range idx.ObjectMap {
		for h := range objs {
			objects[h] = struct{}{}
		}
	}
	return nil
}

func (p *PackPusher) readPackedCommits() (map[entities.Hash]struct{}, error) {
	objCount := 0
	for _, pack := range p.packs.Packs {
		if pack.Type == binding.CommitsPack {
			objCount += pack.ObjectsCount
		}
	}
	packedObjects := make(map[entities.Hash]struct{}, objCount)
	for _, pack := range p.packs.Packs {
		if pack.Type == binding.CommitsPack {
			if err := p.addPackObjects(pack, packedObjects); err != nil {
				return nil, err
			}
		}
	}
	return packedObjects, nil
}

func (p *PackPusher) shouldPack(missings int) bool {
	return missings > minObjectsToPush || time.Since(p.packs.LastUpdateTime) > maxDelayToPush && missings > 0
}

type packInfo struct {
	time     time.Time
	duration time.Duration
	pack     string
	packPath string
	objects  int
	packType binding.PackContentType
}

func (p *PackPusher) packMissings(ctx context.Context, packTime time.Time) (bool, error) {
	packedObjects, err := p.readPackedCommits()
	if err != nil {
		return false, err
	}
	repoObjects, err := p.git.RevListAll(p.gitRoot)
	if err != nil {
		return false, err
	}

	missings := make([]entities.Hash, 0)
	for _, o := range repoObjects {
		if _, ok := packedObjects[o]; !ok {
			missings = append(missings, o)
		}
	}

	if !p.shouldPack(len(missings)) {
		return false, nil // indicates we short-circuit packing
	}

	newPacks, err := p.addPacks(ctx, missings, packTime)
	if err != nil {
		return false, err
	}
	p.packs.Packs = append(p.packs.Packs, newPacks...)
	return true, nil
}

func (*PackPusher) packIndexObjects(objectIDs []entities.Hash, prefix string) (packPath string, retErr error) {
	idx := fastgit.NewIndexFile()
	idx.ObjectCount = len(objectIDs)
	idx.ObjectCRCs = make([]uint32, len(objectIDs))
	idx.ObjectOffsets = make([]int64, len(objectIDs))
	for i, h := range objectIDs {
		idx.ObjectMap[h[0]][h] = i
	}

	name := entities.Hash(sha1.Sum([]byte(time.Now().String())))
	packPath = prefix + "-" + name.String()
	f, err := os.OpenFile(packPath+".idx", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return "", err
	}
	defer entities.CloseErrCheck(f, &retErr)

	if err := fastgit.WritePackIndex(idx, f, name); err != nil {
		return "", err
	}

	if err := os.WriteFile(packPath+".pack", nil, 0644); err != nil {
		return "", err
	}
	return packPath, nil
}

func (p *PackPusher) packObjects(ctx context.Context, objectIDs []entities.Hash, packType binding.PackContentType, packTime time.Time) (binding.PackEntry, error) {
	start := time.Now()

	prefix := fmt.Sprintf("%s/%d", p.packRoot(), time.Now().UnixNano())

	var packPath string
	var err error
	if packType != binding.BlobsListPack {
		packPath, err = p.git.PackObjects(p.gitRoot, prefix, objectIDs)
		if err != nil {
			return binding.PackEntry{}, err
		}
	} else {
		packPath, err = p.packIndexObjects(objectIDs, prefix)
		if err != nil {
			return binding.PackEntry{}, err
		}
		_ = os.Remove(packPath + ".rev")
	}

	pi := &packInfo{
		duration: time.Since(start),
		pack:     strings.TrimPrefix(packPath, prefix+"-"),
		packPath: packPath,
		objects:  len(objectIDs),
		packType: packType,
		time:     packTime,
	}
	return p.uploadPack(ctx, pi)
}

func (p *PackPusher) Push(ctx context.Context) error {
	if p.strategy == binding.PackingStrategyNone {
		return nil
	}

	if err := p.init(ctx); err != nil {
		return err
	}

	if p.packs == nil || p.packs.Strategy != p.strategy {
		return p.createInitPacks(ctx)
	}

	packTime := time.Now()
	if packed, err := p.packMissings(ctx, packTime); !packed || err != nil {
		return err
	}

	compactedPacks, err := p.compactPacks(ctx, p.packs.Packs)
	if err != nil {
		return err
	}

	compacted := false
	if len(p.packs.Packs) > len(compactedPacks) {
		log.Printf("Packs compaction %s@%s before=%d after=%d", p.server, p.project, len(p.packs.Packs), len(compactedPacks))
		compacted = true
	}

	p.packs.Packs = compactedPacks

	if err := p.putPacksObject(ctx); err != nil {
		return err
	}

	if compacted {
		if err := p.cleanPackRoot(compactedPacks); err != nil {
			return err
		}
	}
	return nil
}

func (p *PackPusher) uploadPack(ctx context.Context, packedInfo *packInfo) (binding.PackEntry, error) {
	idxHash, idxSize, err := remote.PushFile(ctx, packedInfo.packPath+".idx", p.client)
	if err != nil {
		return binding.PackEntry{}, err
	}
	if err := os.Rename(packedInfo.packPath+".idx", path.Join(p.packRoot(), packedInfo.pack+".idx")); err != nil {
		return binding.PackEntry{}, err
	}

	packHash, packSize, err := remote.PushFile(ctx, packedInfo.packPath+".pack", p.client)
	if err != nil {
		return binding.PackEntry{}, err
	}
	if err := os.Rename(packedInfo.packPath+".pack", path.Join(p.packRoot(), packedInfo.pack+".pack")); err != nil {
		return binding.PackEntry{}, err
	}

	ent := binding.PackEntry{
		Type:            packedInfo.packType,
		PackID:          packedInfo.pack,
		PackingDuration: packedInfo.duration.Seconds(),
		ObjectsCount:    packedInfo.objects,
		Content:         packHash,
		ContentSize:     packSize,
		Index:           idxHash,
		IndexSize:       idxSize,
		PackedTime:      packedInfo.time.UTC(),
	}
	return ent, nil
}

func (p *PackPusher) compactPacks(ctx context.Context, packs []binding.PackEntry) ([]binding.PackEntry, error) {
	defer func() {
		p.allObjectsForCompaction = nil
	}()

	groups := p.groupPacks(packs)

	result := make([]binding.PackEntry, 0, len(groups))
	for key, grp := range groups {
		packs, err := p.mergePacks(ctx, grp, key)
		if err != nil {
			return nil, err
		}
		result = append(result, packs...)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].PackedTime.Before(result[j].PackedTime)
	})
	return result, nil
}

func (p *PackPusher) mergeSameTypePacks(ctx context.Context, packs []binding.PackEntry, packTime time.Time) (binding.PackEntry, error) {
	log.Printf("Merging %d pack-files for %s@%s", len(packs), p.server, p.project)

	if p.allObjectsForCompaction == nil {
		allObjects, err := p.git.AllObjects(p.gitRoot)
		if err != nil {
			return binding.PackEntry{}, err
		}
		p.allObjectsForCompaction = make(map[entities.Hash]struct{})

		for _, h := range allObjects {
			p.allObjectsForCompaction[h] = struct{}{}
		}
	}

	objsCount := 0
	for _, pack := range packs {
		objsCount += pack.ObjectsCount
	}
	combinedObjects := make([]entities.Hash, 0, objsCount)
	combinedObjectsMap := make(map[entities.Hash]struct{}, objsCount)
	for _, pack := range packs {
		objMap := make(map[entities.Hash]struct{}, pack.ObjectsCount)
		if err := p.addPackObjects(pack, objMap); err != nil {
			return binding.PackEntry{}, err
		}
		for h := range objMap {
			if _, ok := p.allObjectsForCompaction[h]; ok {
				if _, ok := combinedObjectsMap[h]; !ok {
					combinedObjects = append(combinedObjects, h)
					combinedObjectsMap[h] = struct{}{}
				}
			}
		}
	}
	return p.packObjects(ctx, combinedObjects, packs[0].Type, packTime)
}

func (p *PackPusher) mergePacks(ctx context.Context, packs []binding.PackEntry, packTime time.Time) ([]binding.PackEntry, error) {
	groups := make(map[binding.PackContentType][]binding.PackEntry)
	for _, pack := range packs {
		groups[pack.Type] = append(groups[pack.Type], pack)
	}

	result := make([]binding.PackEntry, 0, len(groups))
	for _, grp := range groups {
		if len(grp) > 1 {
			merged, err := p.mergeSameTypePacks(ctx, grp, packTime)
			if err != nil {
				return nil, err
			}
			result = append(result, merged)
		} else {
			grp[0].PackedTime = packTime
			result = append(result, grp[0])
		}
	}

	return result, nil
}

func (p *PackPusher) groupPacks(packs []binding.PackEntry) map[time.Time][]binding.PackEntry {
	now := time.Now().UTC()
	groups := make(map[time.Time][]binding.PackEntry)
	for _, p := range packs {
		k := timeToGroup(p.PackedTime, now)
		groups[k] = append(groups[k], p)
	}

	return groups
}

func timeToGroup(t, now time.Time) time.Time {
	pad := time.Second
	t = t.UTC()
	now = now.UTC()

	if now.Year() > t.Year() {
		return time.Date(t.Year()+1, time.January, 1, 0, 0, 0, 0, time.UTC).Add(-pad)
	}

	if (now.Month()-1)/3 > (t.Month()-1)/3 {
		return time.Date(t.Year(), (1+(t.Month()-1)/3)*3, 1, 0, 0, 0, 0, time.UTC).Add(-pad)
	}

	if now.Month() > t.Month() {
		return time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC).Add(-pad)
	}

	if (now.Day()-1)/5 > (t.Day()-1)/5 {
		return time.Date(t.Year(), t.Month(), (1+(t.Day()-1)/5)*5, 0, 0, 0, 0, time.UTC).Add(-pad)
	}

	if now.Day() > t.Day() {
		return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC).Add(-pad)
	}

	return t
}
