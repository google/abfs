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
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"

	"github.com/google/abfs/shared/entities"
)

type TestConfig struct {
	Foo string `yaml:"foo"`
}

func (c *TestConfig) Validate() error {
	return nil
}

type MockStore struct {
	entities.CASStore
	content []byte
}

func (m *MockStore) OpenBlob(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(m.content)), nil
}

func TestReadConfig(t *testing.T) {
	expected := &TestConfig{Foo: "bar"}
	content, _ := yaml.Marshal(expected)
	store := &MockStore{content: content}

	ctx := context.Background()
	hash := entities.Hash{} // Placeholder hash

	config, err := ReadConfig[*TestConfig](ctx, hash, store)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}

	assert.Equal(t, expected.Foo, config.Foo)
}

func TestBadReadConfig(t *testing.T) {
	store := &MockStore{content: []byte("invalid-yaml")}

	ctx := context.Background()
	hash := entities.Hash{}

	_, err := ReadConfig[*TestConfig](ctx, hash, store)
	if err == nil {
		t.Fatalf("ReadConfig for invalid yaml succeeded")
	}

	assert.ErrorContains(t, err, "remoteconfig.TestConfig")
}
