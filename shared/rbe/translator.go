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
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/rbe/model"
	"github.com/google/abfs/shared/remote"
)

// TranslateDirectory converts an RBE Directory proto into ABFS tree bytes,
// primes the IdentityStore with mappings for all children
// and returns a list of "bundled" blobs (like symlink targets).
func TranslateDirectory(dir *repb.Directory, idStore IdentityStore, currentDigest digest.Digest, gitlinks map[digest.Digest]entities.Hash, sha1Map map[digest.Digest]entities.Hash) ([]byte, []remote.BundledObject, error) {
	treeBytes, bundled, idEntries, err := TranslateDirectoryEntries(dir, currentDigest, gitlinks, sha1Map)
	if err != nil {
		return nil, nil, err
	}
	if idStore != nil && len(idEntries) > 0 {
		if err := idStore.PutMany(idEntries); err != nil {
			return nil, nil, fmt.Errorf("failed to prime identity store: %w", err)
		}
	}
	return treeBytes, bundled, nil
}

// TranslateDirectoryEntries converts an RBE Directory proto into ABFS tree bytes,
// extracts identity store mapping entries without writing to the store directly,
// and returns a list of "bundled" blobs (like symlink targets).
func TranslateDirectoryEntries(dir *repb.Directory, currentDigest digest.Digest, gitlinks map[digest.Digest]entities.Hash, sha1Map map[digest.Digest]entities.Hash) ([]byte, []remote.BundledObject, []IdentityEntry, error) {
	numEntries := len(dir.Files) + len(dir.Directories) + len(dir.Symlinks)
	entries := make([]entities.FileEntry, 0, numEntries)
	bundled := make([]remote.BundledObject, 0, len(dir.Symlinks))
	idEntries := make([]IdentityEntry, 0, numEntries)

	// Process Files
	for _, node := range dir.Files {
		sha1Hash, err := extractSHA1(node.NodeProperties)
		if err != nil {
			// Fallback: Generate a deterministic fake SHA-1 from the RBE SHA-256.
			// TODO: This is only needed until the NodeProperty are
			// properly populate in RBE. Once this is the case, the error will be
			// returned.
			sha1Hash = fallbackFileHash(node.Digest.Hash)
		}

		mode := entities.FileModeRegular
		if node.IsExecutable {
			mode = entities.FileModeExecutable
		}

		idEntries = append(idEntries, IdentityEntry{
			SHA1: sha1Hash,
			Node: model.Node{
				Digest: model.Digest{
					SHA256: node.Digest.Hash,
					Size:   node.Digest.SizeBytes,
				},
				Type: model.NodeTypeFile,
			},
		})

		entries = append(entries, entities.FileEntry{
			Name: node.Name,
			Mode: mode,
			Hash: sha1Hash,
			Size: node.Digest.SizeBytes,
		})
	}

	// Process Directories
	for _, node := range dir.Directories {
		var sha1Hash entities.Hash
		var mode entities.FileMode

		d := digest.Digest{
			Hash: node.Digest.Hash,
			Size: node.Digest.SizeBytes,
		}
		if gHash, ok := gitlinks[d]; ok {
			// Case 1: This is a gitlink properly identified.
			sha1Hash = gHash
			mode = entities.FileModeSubmodule
		} else if sHash, ok := sha1Map[d]; ok {
			// Case 2: This is a regular directory with an explicit SHA-1 stored in its properties.
			sha1Hash = sHash
			mode = entities.FileModeDir
		} else {
			// Case 3: Fallback: Generate a deterministic fake SHA-1 from the RBE SHA-256.
			// TODO: This is only needed until the NodeProperty are
			// properly populate in RBE. Once this is the case, the error will be
			// returned.
			sha1Hash = fallbackDirHash(node.Digest.Hash)
			mode = entities.FileModeDir
		}

		if mode != entities.FileModeSubmodule {
			idEntries = append(idEntries, IdentityEntry{
				SHA1: sha1Hash,
				Node: model.Node{
					Digest: model.Digest{
						SHA256: node.Digest.Hash,
						Size:   node.Digest.SizeBytes,
					},
					Type: model.NodeTypeDirectory,
				},
			})
		}

		entries = append(entries, entities.FileEntry{
			Name: node.Name,
			Mode: mode,
			Hash: sha1Hash,
			Size: node.Digest.SizeBytes,
		})
	}

	// Process Symlinks
	for _, node := range dir.Symlinks {
		sha1Hash, err := extractSHA1(node.NodeProperties)
		if err != nil {
			// TODO: This is only needed until the NodeProperty are
			// properly populate in RBE. Once this is the case, the error will be
			// returned.
			// Symlinks in RBE don't have a data digest in the same way, but they have a target.
			// We'll use the target name to generate a deterministic hash if properties are missing.
			sha1Hash = fallbackSymlinkHash(node.Target)
			log.Printf("(go) TranslateDirectory: missing SHA-1 for symlink %q, using fake: %s\n", node.Name, sha1Hash)
		}

		digest, err := extractDigestFromSymlink(node.NodeProperties)
		if err != nil {
			// Fallback: use target name to generate a deterministic RBE digest.
			h := sha256.Sum256([]byte(node.Target))
			digest = model.Digest{
				SHA256: hex.EncodeToString(h[:]),
				Size:   int64(len(node.Target)),
			}
			log.Printf("(go) TranslateDirectory: missing RBE digest for symlink %q, using fallback: %s/%d\n", node.Name, digest.SHA256, digest.Size)
		}

		idEntries = append(idEntries, IdentityEntry{
			SHA1: sha1Hash,
			Node: model.Node{
				Digest: digest,
				Type:   model.NodeTypeSymlink,
			},
		})

		entries = append(entries, entities.FileEntry{
			Name: node.Name,
			Mode: entities.FileModeSymlink,
			Hash: sha1Hash,
			Size: digest.Size,
		})

		// Add raw target to bundle for Cacheman pre-population.
		// These will be saved as raw blobs in the "blobs/" local store.
		bundled = append(bundled, remote.BundledObject{
			Hash: sha1Hash,
			Data: []byte(node.Target),
		})
	}

	// Determine the directory SHA-1.
	// 1. Try sha1Map (already known, pre-provided, or extracted by the caller).
	treeHash := sha1Map[currentDigest]
	tree, err := gitobjects.EncodeTree(entries)
	// 2. Fallback: Recalculate if still missing.
	// TODO: This is only needed until the NodeProperty are
	// properly populate in RBE. Once this is the case, the error will be
	// returned.
	if treeHash == entities.ZeroHash && err == nil {
		treeHash = entities.Hash(sha1.Sum(tree))
		// Populate the map so sub-directories don't need to recalculate this in recursive calls.
		sha1Map[currentDigest] = treeHash
	}
	return tree, bundled, idEntries, err
}

func extractSHA1(props *repb.NodeProperties) (entities.Hash, error) {
	if props == nil {
		return entities.ZeroHash, fmt.Errorf("node properties missing")
	}
	for _, p := range props.Properties {
		if p.Name == "git_sha1" {
			if entities.IsHash(p.Value) {
				return entities.NewHash(p.Value), nil
			}
			return entities.ZeroHash, fmt.Errorf("invalid sha1 value: %q", p.Value)
		}
	}
	return entities.ZeroHash, fmt.Errorf("sha1 property not found")
}

// extractDigestFromSymlink extracts the RBE digest from node properties.
// The digest is expected as "sha256/size".
func extractDigestFromSymlink(props *repb.NodeProperties) (model.Digest, error) {
	if props == nil {
		return model.Digest{}, fmt.Errorf("node properties missing")
	}
	for _, p := range props.Properties {
		if p.Name == "digest" {
			parts := strings.Split(p.Value, "/")
			if len(parts) != 2 {
				return model.Digest{}, fmt.Errorf("unexpected digest format: %q", p.Value)
			}
			size, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				return model.Digest{}, fmt.Errorf("failed to parse size from %q: %w", p.Value, err)
			}
			return model.Digest{
				SHA256: parts[0],
				Size:   size,
			}, nil
		}
	}
	return model.Digest{}, fmt.Errorf("digest property not found")
}

func extractGitLink(props *repb.NodeProperties) (entities.Hash, error) {
	if props == nil {
		return entities.ZeroHash, fmt.Errorf("node properties missing")
	}
	for _, p := range props.Properties {
		if p.Name == "gitlink" {
			if entities.IsHash(p.Value) {
				return entities.NewHash(p.Value), nil
			}
			return entities.ZeroHash, fmt.Errorf("invalid gitlink value: %q", p.Value)
		}
	}
	return entities.ZeroHash, fmt.Errorf("gitlink property not found")
}
