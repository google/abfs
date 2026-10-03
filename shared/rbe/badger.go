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
	"encoding/json"
	"fmt"

	"github.com/dgraph-io/badger/v4"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/rbe/model"
)

type BadgerIdentityStore struct {
	db   *badger.DB
	path string
}

var _ IdentityStore = (*BadgerIdentityStore)(nil)

// NewBadgerIdentityStore creates and opens a new IdentityStore backed by BadgerDB.
func NewBadgerIdentityStore(path string) (*BadgerIdentityStore, error) {
	opts := badger.DefaultOptions(path)
	// Disable background GC and TTL.
	// We still use Badger's default options but won't initiate manual GC cycles.
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open identity store at %s: %w", path, err)
	}

	return &BadgerIdentityStore{
		db:   db,
		path: path,
	}, nil
}

// Put records a mapping from a SHA-1 hash to an RBE node.
func (s *BadgerIdentityStore) Put(sha1 entities.Hash, node model.Node) error {
	data, err := json.Marshal(node)
	if err != nil {
		return fmt.Errorf("failed to marshal node: %w", err)
	}

	return s.db.Update(func(txn *badger.Txn) error {
		// Key is the raw SHA-1 bytes.
		return txn.Set(sha1[:], data)
	})
}

// PutMany records multiple mappings from SHA-1 hashes to RBE nodes using WriteBatch.
func (s *BadgerIdentityStore) PutMany(entries []IdentityEntry) error {
	if len(entries) == 0 {
		return nil
	}

	wb := s.db.NewWriteBatch()
	defer wb.Cancel()

	for _, entry := range entries {
		data, err := json.Marshal(entry.Node)
		if err != nil {
			return fmt.Errorf("failed to marshal node: %w", err)
		}
		if err := wb.Set(entry.SHA1[:], data); err != nil {
			return fmt.Errorf("failed to write identity entry for %s: %w", entry.SHA1, err)
		}
	}

	if err := wb.Flush(); err != nil {
		return fmt.Errorf("failed to flush identity store write batch: %w", err)
	}
	return nil
}

// Get retrieves the RBE node associated with a SHA-1 hash.
func (s *BadgerIdentityStore) Get(sha1 entities.Hash) (model.Node, bool, error) {
	var node model.Node
	var found bool

	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(sha1[:])
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return nil
			}
			return err
		}

		found = true
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &node)
		})
	})

	if err != nil {
		return model.Node{}, false, fmt.Errorf("failed to get mapping for %s: %w", sha1, err)
	}

	return node, found, nil
}

// Close closes the underlying BadgerDB instance.
func (s *BadgerIdentityStore) Close() error {
	return s.db.Close()
}
