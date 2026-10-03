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

package servers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/remote"
	abfsstore "github.com/google/abfs/store"
)

type aclEntry struct {
	Path string `json:"path"`

	// Valid when Path ends with 'default.json'.
	Patterns map[string]remote.ACL `json:"patterns,omitempty"`

	remote.ACL
}

type serverProjectACLs struct {
	Server  string     `json:"server"`
	Project string     `json:"project"`
	ACLs    []aclEntry `json:"acls"`
}

type node struct {
	isDir    bool
	data     []byte
	children map[string]*node
}

// loadTree materializes the directory tree in memory.
// ACL trees are very small, so this is expected to be cheap.
func loadTree(ctx context.Context, store entities.CASStore, hash entities.Hash) (*node, error) {
	hdr, err := store.ObjectHeader(ctx, hash)
	if err != nil {
		return nil, err
	}
	n := &node{}
	if hdr.Type == entities.ObjectType(gitobjects.ObjectTypeDir) {
		n.isDir = true
		n.children = make(map[string]*node)
		buf, _, err := store.GetTree(ctx, hash)
		if err != nil {
			return nil, err
		}
		entries, err := entities.DecodeDirPlain(buf)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			child, err := loadTree(ctx, store, entry.Hash)
			if err != nil {
				return nil, err
			}
			n.children[entry.Name] = child
		}
	} else {
		n.isDir = false
		b, err := abfsstore.ReadAllBlob(ctx, store, hash)
		if err != nil {
			return nil, err
		}
		n.data = b
	}
	return n, nil
}

// setFile updates the file in the tree with the given path and data.
func (n *node) setFile(path string, data []byte) {
	parts := strings.Split(path, "/")
	curr := n
	for i := 0; i < len(parts)-1; i++ {
		part := parts[i]
		if part == "" || part == "." {
			continue
		}
		if curr.children == nil {
			curr.children = make(map[string]*node)
			curr.isDir = true
		}
		child, ok := curr.children[part]
		if !ok {
			child = &node{isDir: true, children: make(map[string]*node)}
			curr.children[part] = child
		}
		curr = child
	}
	leaf := parts[len(parts)-1]
	if curr.children == nil {
		curr.children = make(map[string]*node)
		curr.isDir = true
	}
	curr.children[leaf] = &node{isDir: false, data: data}
}

// putNodeToStore will write the entire node rooted at n into the store
// in a bottom-up fashion.  This avoid missing-link errors on Put()
// and is lighter weight than dagsync.
func putNodeToStore(ctx context.Context, store entities.CASStore, n *node) (entities.Hash, error) {
	if !n.isDir {
		hash := objects.HashBlobBytes(n.data)
		err := store.WriteBlob(ctx, hash, int64(len(n.data)), bytes.NewReader(n.data))
		return hash, err
	}

	var entries []entities.FileEntry
	for name, child := range n.children {
		childHash, err := putNodeToStore(ctx, store, child)
		if err != nil {
			return entities.Hash{}, err
		}
		mode := entities.FileModeRegular
		if child.isDir {
			mode = entities.FileModeDir
		}
		entries = append(entries, entities.FileEntry{
			Name: name,
			Mode: mode,
			Hash: childHash,
		})
	}

	encoded, err := gitobjects.EncodeTree(entries)
	if err != nil {
		return entities.Hash{}, err
	}
	treeHash := objects.HashBytes(encoded)
	err = store.PutObject(ctx, treeHash, encoded)
	return treeHash, err
}

// LoadAndSetACLs reads the specified ACL bootstrap JSON file and applies the entries.
// Note: This behavior is declarative on a per-file level. Other files in the ACL
// directory tree that are not mentioned in the bootstrap file are left completely
// untouched. However, any specific file mentioned in the bootstrap file will have
// its content completely overwritten by the new definition.
func LoadAndSetACLs(ctx context.Context, prov remote.RemoteCloser, aclFilePath string) error {
	content, err := os.ReadFile(aclFilePath)
	if err != nil {
		return fmt.Errorf("failed to read ACL file %q: %w", aclFilePath, err)
	}

	var acls []serverProjectACLs
	if err := json.Unmarshal(content, &acls); err != nil {
		return fmt.Errorf("failed to parse ACL JSON: %w", err)
	}

	store := remote.NewRemoteStore(remote.NewCompressingCloser(remote.NopCloser{RemoteProvider: prov}))

	for _, a := range acls {
		// Load existing acl ref if it exists.
		hashes, err := store.GetRefs(a.Server, a.Project, []string{remote.ACLRef})
		var rootNode *node
		if err == nil && len(hashes) > 0 && hashes[0] != entities.ZeroHash {
			rootNode, err = loadTree(ctx, store, hashes[0])
			if err != nil {
				return fmt.Errorf("failed to load existing ACL tree for %s/%s: %w", a.Server, a.Project, err)
			}
		} else {
			rootNode = &node{isDir: true, children: make(map[string]*node)}
		}

		for _, entry := range a.ACLs {
			var data []byte
			if strings.HasSuffix(entry.Path, remote.DefaultACLFile) {
				pacl := remote.PatternedACL{Patterns: entry.Patterns}
				if err := pacl.Validate(); err != nil {
					return fmt.Errorf("invalid patterned ACL for path %s in %s/%s: %w", entry.Path, a.Server, a.Project, err)
				}
				data, err = json.MarshalIndent(pacl, "", "  ")
			} else {
				acl := entry.ACL
				data, err = json.MarshalIndent(acl, "", "  ")
			}
			if err != nil {
				return fmt.Errorf("failed to marshal ACL data: %w", err)
			}

			rootNode.setFile(entry.Path, data)
		}

		// Persist new node recursively to the store.
		newRootHash, err := putNodeToStore(ctx, store, rootNode)
		if err != nil {
			return fmt.Errorf("failed to write new ACL tree for %s/%s: %w", a.Server, a.Project, err)
		}

		if err := store.PutRefs(a.Server, a.Project, []entities.RefHash{
			{Ref: remote.ACLRef, Hash: newRootHash},
		}); err != nil {
			return fmt.Errorf("failed to put ACL ref for %s/%s: %w", a.Server, a.Project, err)
		}
	}

	return nil
}
