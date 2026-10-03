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

package server

import (
	"github.com/google/abfs/shared/entities"
)

const DefaultTTLHours = 8

type MapsStore interface {
	// Open must be called on any store prior to use.
	Open() error

	// Get returns all mappings for 'from'.
	Get(from entities.Hash) ([]entities.Hash, error)

	// Put adds a 'from'->'to' mapping.
	Put(from, to entities.Hash) error

	// Close closes the store.
	Close() error
}

type Maps struct {
	mapsStore MapsStore
}

func NewMaps(mapsStore MapsStore) (*Maps, error) {
	maps := &Maps{
		mapsStore: mapsStore,
	}
	if err := maps.mapsStore.Open(); err != nil {
		return nil, err
	}

	return maps, nil
}

// Get returns all 'to' targets for a key in non-deterministic order.
func (m *Maps) Get(key entities.Hash) ([]entities.Hash, error) {
	return m.mapsStore.Get(key)
}

func (m *Maps) Put(from, to entities.Hash) error {
	return m.mapsStore.Put(from, to)
}

func (m *Maps) Close() error {
	return m.mapsStore.Close()
}
