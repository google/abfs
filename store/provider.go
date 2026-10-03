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

package store

import (
	"context"
	"time"

	"github.com/google/abfs/cas/cas/providers/cacheman"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/entities"
)

type Provider struct {
	client      *client.Client
	casProvider *cacheman.Provider
	localOnly   bool
}

func NewProvider(cacheDir string, fetchTimeout time.Duration, localOnly bool) *Provider {
	cli := client.NewClient(cacheDir, fetchTimeout)
	return &Provider{
		client:      cli,
		casProvider: cacheman.NewProviderWithClient(cli.CasClient()),
		localOnly:   localOnly,
	}
}

func (p *Provider) Close() error {
	return p.casProvider.Close()
}

func (p *Provider) Ping() error {
	return p.client.Ping()
}

func (p *Provider) PingBinary() error {
	return p.client.PingBinary()
}

func (p *Provider) Start() error {
	return p.casProvider.Open()
}
func (p *Provider) FetchBlob(ctx context.Context, key entities.Hash) error {
	return p.casProvider.FetchFile(bindcas.CASHash(&key))
}

func (p *Provider) FetchTree(ctx context.Context, key entities.Hash) error {
	return p.casProvider.FetchDirEntries(bindcas.CASHash(&key))
}

func (p *Provider) FetchObject(ctx context.Context, key entities.Hash) error {
	return p.casProvider.FetchDirEntries(bindcas.CASHash(&key))
}

func (p *Provider) NewBlob(ctx context.Context, key entities.Hash) error {
	return p.casProvider.NewFile(bindcas.CASHash(&key))
}

func (p *Provider) NewTree(ctx context.Context, key entities.Hash) error {
	return p.casProvider.NewDirEntries(bindcas.CASHash(&key))
}
func (p *Provider) SendNotifyUse(key entities.Hash) error {
	return p.casProvider.Accessed(bindcas.CASHash(&key))
}
func (p *Provider) NewObject(ctx context.Context, key entities.Hash) error {
	return p.client.SendNewObject(key, cacheman.TypeObject)
}
func (p *Provider) AddMapResult(ctx context.Context, from, to entities.Hash) error {
	return p.client.SendNewMap(from, to, p.localOnly)
}

func (p *Provider) MapResults(ctx context.Context, id entities.Hash) ([]entities.Hash, error) {
	return p.client.MapResults(id)
}

func (p *Provider) FetchMapResults(ctx context.Context, id entities.Hash) error {
	return p.client.FetchMapRequest(id)
}

func (p *Provider) GetRefs(server string, project string, refs []string) ([]entities.Hash, error) {
	return p.client.GetRefs(server, project, refs)
}

func (p *Provider) PutRefs(server string, project string, refs []entities.RefHash, blocking bool) error {
	return p.client.PutRefs(server, project, refs, blocking)
}

func (p *Provider) SearchRefs(server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	return p.client.SearchRefs(server, project, refPrefix, limit)
}

func (p *Provider) FetchGitObject(ctx context.Context, key entities.Hash) error {
	result, err := p.client.FetchGitObjectRequest(key)
	if err != nil {
		return err
	}
	return result.ToError()
}

func (p *Provider) FetchStat(ctx context.Context, key entities.Hash) (entities.StatInfo, error) {
	return p.client.FetchStatRequest(key)
}

func (p *Provider) KeepAlive(ctx context.Context, key entities.Hash) error {
	return p.client.KeepAlive(key)
}

func (p *Provider) IsObjectRemote(ctx context.Context, key entities.Hash) (bool, error) {
	return p.client.IsObjectRemote(key)
}

func (p *Provider) StartGC(ctx context.Context) error {
	return p.client.StartGC()
}
