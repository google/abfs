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

package rbe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	"github.com/bazelbuild/remote-apis-sdks/go/pkg/rexec"
	"github.com/bazelbuild/remote-apis-sdks/go/pkg/uploadinfo"
	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"google.golang.org/protobuf/proto"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/rbe/model"
	"github.com/google/abfs/shared/remote"
)

// RBEClient implements the remote.RemoteProvider interface using an RBE backend.
// It uses an IdentityStore to map ABFS SHA-1 hashes to RBE SHA-256 digests.
type RBEClient struct {
	rbeClient     *rexec.Client
	identityStore IdentityStore
}

var _ remote.RemoteProvider = (*RBEClient)(nil)

// NewRBEClient creates a new RBEClient.
func NewRBEClient(rbeClient *rexec.Client, idStore IdentityStore) *RBEClient {
	return &RBEClient{
		rbeClient:     rbeClient,
		identityStore: idStore,
	}
}

// GetObject fulfills a SHA-1 request by resolving it via the IdentityStore and fetching from RBE.
func (c *RBEClient) GetObject(ctx context.Context, key entities.Hash) (remote.ContentReadCloser, error) {
	d, found, err := c.identityStore.Get(key)
	if err != nil {
		return nil, fmt.Errorf("identity lookup failed for %s: %w", key, err)
	}

	if !found {
		return nil, entities.NotExistErrf("cannot find object %s", key)
	}

	switch d.Type {
	case model.NodeTypeDeepTree:
		return nil, entities.NotExistErrf("deeptree %s is not stored in RBE", key)

	case model.NodeTypeFile, model.NodeTypeSymlink:
		rbeDigest := digest.Digest{
			Hash: d.Digest.SHA256,
			Size: d.Digest.Size,
		}

		// We use GrpcClient.ReadBlob to fetch the raw bytes (file contents or symlink target).
		blob, _, err := c.rbeClient.GrpcClient.ReadBlob(ctx, rbeDigest)
		if err != nil {
			return nil, fmt.Errorf("RBE fetch failed for %s %s (SHA256: %s): %w", d.Type, key, d.Digest.SHA256, err)
		}

		// For Blobs (files and symlinks), ABFS expects the header as well in GetObject.
		return remote.NewBlobReaderFromBytes(blob), nil

	case model.NodeTypeDirectory:
		rbeDigest := digest.Digest{
			Hash: d.Digest.SHA256,
			Size: d.Digest.Size,
		}
		// Fetch the RBE Directory tree.
		dirs, err := c.rbeClient.GrpcClient.GetDirectoryTree(ctx, rbeDigest.ToProto())
		if err != nil {
			return nil, fmt.Errorf("RBE fetch failed for directory %s: %w", key, err)
		}
		if len(dirs) == 0 {
			return nil, fmt.Errorf("RBE returned empty directory tree for %s", key)
		}

		// 1. Index all returned directories by their RBE digest.
		// Also extract any Git submodule hashes (gitlinks) and explicit Git SHA-1 properties.
		protoMap, gitlinkMap, sha1Map, err := indexRBEDirectories(dirs)
		if err != nil {
			return nil, err
		}

		// Fallback to the passed key if rootGitHash is missing in sha1Map.
		if _, ok := sha1Map[rbeDigest]; !ok {
			sha1Map[rbeDigest] = key
		}

		// 2. Translate all directories and compile DeepEntries in a single DFS pass.
		compiler := &deeptreeCompiler{
			client:     c,
			protoMap:   protoMap,
			gitlinkMap: gitlinkMap,
			sha1Map:    sha1Map,
			rootDigest: rbeDigest,
		}
		if err := compiler.compile(); err != nil {
			return nil, err
		}

		// 3. Compile and save the DeepTree locally.
		deeptreeObj, deeptreeMap, err := compileDeepTreeSidecars(key, compiler.deepEntries)
		if err != nil {
			return nil, err
		}

		var bundledMaps []remote.BundledMapResult
		bundledMaps = append(bundledMaps, *deeptreeMap)

		// If the requested key differs from the actual translated root tree hash,
		// also map the actual root tree hash to the same DeepTree.
		actualTreeHash := compiler.sha1Map[rbeDigest]
		if actualTreeHash != key {
			_, actualTreeDecMap, err := compileDeepTreeSidecars(actualTreeHash, compiler.deepEntries)
			if err != nil {
				return nil, fmt.Errorf("failed to compile secondary deeptree map: %w", err)
			}
			bundledMaps = append(bundledMaps, *actualTreeDecMap)
		}

		var bundledObjects []remote.BundledObject
		bundledObjects = append(bundledObjects, *deeptreeObj)
		bundledObjects = append(bundledObjects, compiler.bundledObjects...)

		return remote.NewBundledReader(
			remote.NewReaderFromBytes(compiler.rootTreeBytes),
			compiler.bundledBlobs,
			bundledObjects,
			bundledMaps,
		), nil

	default:
		return nil, fmt.Errorf("unknown node type: %v", d.Type)
	}
}

// PutObject uploads an object to RBE and records the SHA-1 -> SHA-256 mapping.
func (c *RBEClient) PutObject(ctx context.Context, key entities.Hash, reader remote.ContentReadCloser) error {
	// ABFS objects in PutObject are prefixed with their header.
	objType, size, err := objects.ReadHeader(reader)
	if err != nil {
		return fmt.Errorf("failed to read ABFS header: %w", err)
	}

	if objType != string(entities.ObjectTypeBlob) {
		return fmt.Errorf("object type %v is not supported for PutObject", objType)
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("failed to read object data: %w", err)
	}

	if int64(len(data)) != size {
		return fmt.Errorf("object size mismatch: expected %d, got %d", size, len(data))
	}

	// Calculate SHA-256 for RBE.
	h := sha256.Sum256(data)
	sha256Hex := hex.EncodeToString(h[:])

	// Upload to RBE CAS.
	ue := uploadinfo.EntryFromBlob(data)
	_, _, err = c.rbeClient.GrpcClient.UploadIfMissing(ctx, ue)
	if err != nil {
		return fmt.Errorf("RBE upload failed for %s: %w", key, err)
	}

	// Prime the IdentityStore.
	return c.identityStore.Put(key, model.Node{
		Digest: model.Digest{
			SHA256: sha256Hex,
			Size:   size,
		},
		Type: objectTypeToNodeType(entities.ObjectType(objType)),
	})
}

// Headers returns metadata for the provided keys by resolving them via the IdentityStore.
func (c *RBEClient) Headers(ctx context.Context, keys []entities.Hash) ([]entities.ObjectHeader, error) {
	headers := make([]entities.ObjectHeader, len(keys))
	for i, key := range keys {
		d, found, err := c.identityStore.Get(key)
		if err != nil {
			return nil, err
		}
		if found {
			headers[i] = entities.ObjectHeader{
				Type: nodeTypeToObjectType(d.Type),
				Size: d.Digest.Size,
			}
		} else {
			return nil, entities.ErrNotExist
		}
	}
	return headers, nil
}

// MapResults delegates to fallback.
func (c *RBEClient) MapResults(ctx context.Context, from entities.Hash) ([]entities.Hash, error) {
	return nil, nil
}

// AddMapResult delegates to fallback.
func (c *RBEClient) AddMapResult(ctx context.Context, from, to entities.Hash) error {
	return nil
}

// GetRefs delegates to fallback.
func (c *RBEClient) GetRefs(ctx context.Context, server string, project string, refs []string) ([]entities.Hash, error) {
	return nil, nil
}

// SearchRefs delegates to fallback.
func (c *RBEClient) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	return nil, nil
}

// PutRefs delegates to fallback.
func (c *RBEClient) PutRefs(ctx context.Context, server string, project string, refs []entities.RefHash) error {
	return nil
}

// Mark delegates to fallback.
func (c *RBEClient) Mark(ctx context.Context, hash entities.Hash, key, value string) (int64, error) {
	return 0, nil
}

// Version returns version information.
func (c *RBEClient) Version(ctx context.Context) (string, error) {
	return "RBE-Bridge-v1", nil
}

// WhoAmI returns identity information.
func (c *RBEClient) WhoAmI(ctx context.Context) (string, error) {
	return "RBEClient", nil
}

// SHA256 is unimplemented.
func (c *RBEClient) SHA256(ctx context.Context, keys []entities.Hash) ([]entities.SHA256, error) {
	return nil, errors.New("not implemented")
}

func (c *RBEClient) Close() error {
	if c.rbeClient != nil && c.rbeClient.GrpcClient != nil {
		return c.rbeClient.GrpcClient.Close()
	}
	return nil
}

// Scans RBE Directory protobufs and indexes them by their SHA-256 digest.
// It also extracts and maps any explicitly defined Git SHA-1 hashes (for gitlinks and directories)
// from the node properties.
func indexRBEDirectories(dirs []*repb.Directory) (
	protoMap map[digest.Digest]*repb.Directory,
	gitlinkMap map[digest.Digest]entities.Hash,
	sha1Map map[digest.Digest]entities.Hash,
	err error,
) {
	protoMap = make(map[digest.Digest]*repb.Directory)
	gitlinkMap = make(map[digest.Digest]entities.Hash)
	sha1Map = make(map[digest.Digest]entities.Hash)

	for _, dirProto := range dirs {
		protoData, _ := proto.Marshal(dirProto)
		h := sha256.Sum256(protoData)
		d := digest.Digest{
			Hash: hex.EncodeToString(h[:]),
			Size: int64(len(protoData)),
		}
		protoMap[d] = dirProto

		if gHash, err := extractGitLink(dirProto.NodeProperties); err == nil {
			if len(dirProto.Directories) == 0 && len(dirProto.Files) == 0 && len(dirProto.Symlinks) == 0 {
				gitlinkMap[d] = gHash
			} else {
				return nil, nil, nil, fmt.Errorf("directory %s has gitlink property but is not empty", d)
			}
		} else if sHash, err := extractSHA1(dirProto.NodeProperties); err == nil {
			sha1Map[d] = sHash
		}
	}
	return protoMap, gitlinkMap, sha1Map, nil
}

type deeptreeCompiler struct {
	client          *RBEClient
	protoMap        map[digest.Digest]*repb.Directory
	gitlinkMap      map[digest.Digest]entities.Hash
	sha1Map         map[digest.Digest]entities.Hash
	deepEntries     []*entities.DeepEntry
	identityEntries []IdentityEntry
	bundledObjects  []remote.BundledObject
	bundledBlobs    []remote.BundledObject
	rootTreeBytes   []byte
	rootDigest      digest.Digest
}

func (c *deeptreeCompiler) compile() error {
	if _, err := c.dfs(c.rootDigest, ""); err != nil {
		return err
	}
	return c.flushIdentityEntries()
}

func (c *deeptreeCompiler) addIdentityEntries(entries []IdentityEntry) error {
	if c.client.identityStore == nil || len(entries) == 0 {
		return nil
	}
	c.identityEntries = append(c.identityEntries, entries...)
	if len(c.identityEntries) >= 50000 {
		return c.flushIdentityEntries()
	}
	return nil
}

func (c *deeptreeCompiler) flushIdentityEntries() error {
	if c.client.identityStore == nil || len(c.identityEntries) == 0 {
		c.identityEntries = c.identityEntries[:0]
		return nil
	}
	if err := c.client.identityStore.PutMany(c.identityEntries); err != nil {
		return fmt.Errorf("failed to commit identity store entries: %w", err)
	}
	c.identityEntries = c.identityEntries[:0]
	return nil
}

func (c *deeptreeCompiler) dfs(d digest.Digest, name string) (*entities.DeepEntry, error) {
	dirProto := c.protoMap[d]
	if dirProto == nil {
		return nil, fmt.Errorf("missing directory proto for digest %s", d)
	}

	// A. Translate directory to Git format
	treeBytes, bundledSymlinks, idEntries, err := TranslateDirectoryEntries(dirProto, d, c.gitlinkMap, c.sha1Map)
	if err != nil {
		return nil, fmt.Errorf("failed to translate RBE directory %s: %w", d, err)
	}

	// Uses the guaranteed hash from sha1Map.
	treeHash := c.sha1Map[d]

	// Queue directory identity mapping only for root (non-root directories are already
	// queued by their parent's TranslateDirectoryEntries call).
	if d == c.rootDigest {
		idEntries = append(idEntries, IdentityEntry{
			SHA1: treeHash,
			Node: model.Node{
				Digest: model.Digest{SHA256: d.Hash, Size: d.Size},
				Type:   model.NodeTypeDirectory,
			},
		})
	}
	if err := c.addIdentityEntries(idEntries); err != nil {
		return nil, fmt.Errorf("failed to buffer identity store entries: %w", err)
	}

	if d == c.rootDigest {
		c.rootTreeBytes = treeBytes
		c.bundledBlobs = append(c.bundledBlobs, bundledSymlinks...)
	} else {
		c.bundledObjects = append(c.bundledObjects, remote.BundledObject{
			Hash: treeHash,
			Data: treeBytes,
		})
		c.bundledBlobs = append(c.bundledBlobs, bundledSymlinks...)
	}

	// B. Create the DeepEntry for this directory
	dirEntry := &entities.DeepEntry{
		Name: name,
		Mode: entities.FileModeDir,
		Hash: treeHash,
	}
	c.deepEntries = append(c.deepEntries, dirEntry)
	dirEntry.Size = 1 // count itself

	c.addFiles(dirProto, dirEntry)
	c.addSymlinks(dirProto, dirEntry)

	if err := c.addSubdirectories(dirProto, dirEntry); err != nil {
		return nil, err
	}

	return dirEntry, nil
}

func (c *deeptreeCompiler) addFiles(dirProto *repb.Directory, dirEntry *entities.DeepEntry) {
	for _, node := range dirProto.Files {
		sha1Hash, err := extractSHA1(node.NodeProperties)
		if err != nil {
			sha1Hash = fallbackFileHash(node.Digest.Hash)
		}
		mode := entities.FileModeRegular
		if node.IsExecutable {
			mode = entities.FileModeExecutable
		}
		c.deepEntries = append(c.deepEntries, &entities.DeepEntry{
			Name: node.Name,
			Mode: mode,
			Hash: sha1Hash,
			Size: node.Digest.SizeBytes,
		})
		dirEntry.Size++
	}
}

func (c *deeptreeCompiler) addSymlinks(dirProto *repb.Directory, dirEntry *entities.DeepEntry) {
	for _, node := range dirProto.Symlinks {
		sha1Hash, err := extractSHA1(node.NodeProperties)
		if err != nil {
			sha1Hash = fallbackSymlinkHash(node.Target)
		}
		c.deepEntries = append(c.deepEntries, &entities.DeepEntry{
			Name: node.Name,
			Mode: entities.FileModeSymlink,
			Hash: sha1Hash,
			Size: int64(len(node.Target)),
		})
		dirEntry.Size++
	}
}

func (c *deeptreeCompiler) addSubdirectories(dirProto *repb.Directory, dirEntry *entities.DeepEntry) error {
	for _, node := range dirProto.Directories {
		subD, err := digest.NewFromProto(node.Digest)
		if err != nil {
			return err
		}

		// Submodules are treated as leaf entries and added directly without recursion.
		if gHash, ok := c.gitlinkMap[subD]; ok {
			c.deepEntries = append(c.deepEntries, &entities.DeepEntry{
				Name: node.Name,
				Mode: entities.FileModeSubmodule,
				Hash: gHash,
				Size: 0,
			})
			dirEntry.Size++
			continue
		}

		// Regular directories are traversed recursively.
		if _, ok := c.sha1Map[subD]; !ok {
			// Fix Concern 3: Calculate fallback and write it back to c.sha1Map so that
			// parent/child translations agree and are fully consistent.
			c.sha1Map[subD] = fallbackDirHash(node.Digest.Hash)
		}

		child, err := c.dfs(subD, node.Name)
		if err != nil {
			return err
		}
		dirEntry.Size += child.Size
	}
	return nil
}

// Serializes a list of DeepEntries into a binary DeepTreeObject.
// It returns a BundledObject containing the serialized payload, and a BundledMapResult linking the original
// root directory hash to the newly compiled DeepTree hash.
func compileDeepTreeSidecars(key entities.Hash, deepEntries []*entities.DeepEntry) (*remote.BundledObject, *remote.BundledMapResult, error) {
	deeptree := &gitobjects.DeepTreeObject{
		DeepTree: entities.DeepTree{
			Entries: deepEntries,
		},
	}
	if err := deeptree.Encode(); err != nil {
		return nil, nil, fmt.Errorf("failed to encode compiled deeptree: %w", err)
	}

	req := &objects.DeepTreeRequest{
		Version: objects.CurrentDeepTreeRequestVersion,
		Tree:    key,
	}
	if err := req.Encode(); err != nil {
		return nil, nil, fmt.Errorf("failed to encode deeptree request: %w", err)
	}

	mapResult := &remote.BundledMapResult{
		From: req.Hash,
		To:   deeptree.Hash,
	}

	obj := &remote.BundledObject{
		Hash: deeptree.Hash,
		Data: deeptree.Encoded,
	}

	return obj, mapResult, nil
}

func nodeTypeToObjectType(t model.NodeType) entities.ObjectType {
	switch t {
	case model.NodeTypeFile:
		return entities.ObjectTypeBlob
	case model.NodeTypeDirectory:
		return gitobjects.ObjectTypeDir
	case model.NodeTypeDeepTree:
		return gitobjects.ObjectTypeDeepTree
	default:
		return entities.ObjectTypeUnknown
	}
}

func objectTypeToNodeType(t entities.ObjectType) model.NodeType {
	switch t {
	case gitobjects.ObjectTypeDir:
		return model.NodeTypeDirectory
	case gitobjects.ObjectTypeDeepTree:
		return model.NodeTypeDeepTree
	default:
		return model.NodeTypeFile
	}
}
