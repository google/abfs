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
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/rbe/model"
)

// IdentityEntry represents a single mapping between a SHA-1 hash and an RBE node.
type IdentityEntry struct {
	SHA1 entities.Hash
	Node model.Node
}

// IdentityStore provides a shared mapping layer between ABFS SHA-1 hashes
// and RBE digests (SHA-256 + Size).
type IdentityStore interface {
	// Put records a mapping from a SHA-1 hash to an RBE node (digest + type).
	Put(sha1 entities.Hash, node model.Node) error

	// PutMany records multiple mappings from SHA-1 hashes to RBE nodes in batches.
	PutMany(entries []IdentityEntry) error

	// Get retrieves the RBE node associated with a SHA-1 hash.
	// Returns (node, true, nil) if found, (empty, false, nil) if not found.
	Get(sha1 entities.Hash) (model.Node, bool, error)

	// Close closes the underlying storage.
	Close() error
}
