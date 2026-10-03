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
	"context"
	"errors"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
)

type localOnlyClient struct {
}

func (l *localOnlyClient) Close() error {
	return nil
}

func (l *localOnlyClient) AddMapResult(ctx context.Context, from, to entities.Hash) error {
	return errors.New("AddMapResult unsupported with local only client")
}

func (l *localOnlyClient) GetObject(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
	return nil, errors.New("GetObject unsupported with local only client")
}

func (l *localOnlyClient) GetRefs(ctx context.Context, server string, project string, refs []string) ([]entities.Hash, error) {
	return nil, errors.New("GetRefs unsupported with local only client")
}

func (l *localOnlyClient) Headers(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error) {
	return nil, errors.New("Headers unsupported with local only client")
}

func (l *localOnlyClient) SHA256(ctx context.Context, keys []remote.Hash) ([]entities.SHA256, error) {
	return nil, errors.New("SHA256 unsupported with local only client")
}

func (l *localOnlyClient) MapResults(ctx context.Context, id entities.Hash) ([]entities.Hash, error) {
	return nil, errors.New("MapResults unsupported with local only client")
}

func (l *localOnlyClient) PutObject(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) error {
	return errors.New("PutObject unsupported with local only client")
}

func (l *localOnlyClient) PutRefs(ctx context.Context, server string, project string, refs []entities.RefHash) error {
	return errors.New("PutRefs unsupported with local only client")
}

func (l *localOnlyClient) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	return nil, errors.New("SearchRefs unsupported with local only client")
}

func (l *localOnlyClient) Mark(ctx context.Context, hash entities.Hash, key string, value string) (int64, error) {
	return 0, errors.New("Mark unsupported with local only client")
}

func (l *localOnlyClient) Version(ctx context.Context) (string, error) {
	return "", errors.New("Version unsupported with local only client")
}

func (l *localOnlyClient) WhoAmI(ctx context.Context) (string, error) {
	return "", errors.New("WhoAmI unsupported with local only client")
}

type aresSupported struct {
	remote.ARESService
}

func (*aresSupported) Support() bool {
	return true
}

type emptyARES struct{}

func (*emptyARES) Close() error {
	return nil
}

func (*emptyARES) Support() bool {
	return false
}

func (*emptyARES) Execute(_ context.Context, _ entities.Hash, _ []entities.Hash, _, _ bool) (entities.Hash, error) {
	return entities.ZeroHash, errors.New("remote execution is not currently supported")
}
