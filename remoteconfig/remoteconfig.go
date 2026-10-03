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

package remoteconfig

import (
	"context"
	"fmt"
	"log"

	"gopkg.in/yaml.v3"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/hashpath"
)

// TODO add Write and Update Config

type RemoteConfig interface {
	Validate() error
}

type NewConfig[T RemoteConfig] func(T, entities.Hash)

func WatchConfig[T RemoteConfig](ctx context.Context, hp *hashpath.HashPath, store entities.CASStore, newConfig NewConfig[T]) {
	hashpath.Watch(ctx, hp, store, func(hash entities.Hash, err error) {
		var config T
		if err != nil {
			log.Printf("cannot read %T config: %v", config, err)
			return
		}
		config, err = ReadConfig[T](ctx, hash, store)
		if err != nil {
			log.Printf("failed to read %T config: %v", config, err)
			return
		}
		if err := config.Validate(); err != nil {
			log.Printf("ignoring invalid %T config hash %s: %v", config, hash, err)
			return
		}
		newConfig(config, hash)
	})
}

func ReadConfig[T RemoteConfig](ctx context.Context, hash entities.Hash, store entities.CASStore) (c T, retErr error) {
	var config T
	reader, err := store.OpenBlob(ctx, hash)
	if err != nil {
		return config, fmt.Errorf("failed to open %T config %s: %w", config, hash, err)
	}
	defer entities.CloseErrCheck(reader, &retErr)

	if err := yaml.NewDecoder(reader).Decode(&config); err != nil {
		return config, fmt.Errorf("failed to decode %T config %s: %w", config, hash, err)
	}

	return config, nil
}

func ReadConfigFromHP[T RemoteConfig](ctx context.Context, hp *hashpath.HashPath, store entities.CASStore) (T, error) {
	var config T
	res, err := hashpath.Resolve(ctx, store, hp, entities.ABFSMetaServer, entities.ABFSMetaProject)
	if err != nil {
		return config, fmt.Errorf("failed to get %T config hash from %s: %w", config, hp, err)
	}

	return ReadConfig[T](ctx, res.Resolved, store)
}
