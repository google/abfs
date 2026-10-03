// Copyright 2025 Google LLC
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

package cacheman

import (
	"context"
	"time"

	"github.com/google/abfs/cas/hash"
)

type Provider struct {
	client *Client
}

func NewProvider(socketPath string, fetchTimeout time.Duration) *Provider {
	return NewProviderWithClient(NewClient(socketPath, fetchTimeout))
}
func NewProviderWithClient(c *Client) *Provider {
	p := &Provider{client: c}

	return p
}
func (p *Provider) Open() error {
	return p.client.Serve(context.TODO())
}
func (p *Provider) Close() error {
	return p.client.Close()

}

func (p *Provider) Client() *Client {
	return p.client
}
func returnErr(result ResultType, err error) error {
	if err != nil {
		return err
	}
	return result.ToError()

}
func (p *Provider) FetchFile(h hash.Hash) error {
	return returnErr(p.client.FetchObjectRequest(h, TypeBlob))

}
func (p *Provider) FetchSymLink(h hash.Hash) error {
	return returnErr(p.client.FetchObjectRequest(h, TypeBlob))

}
func (p *Provider) FetchDirList(h hash.Hash) error {
	return nil
}
func (p *Provider) FetchDirEntries(h hash.Hash) error {
	return returnErr(p.client.FetchObjectRequest(h, TypeTree))
}
func (p *Provider) FetchDeepTree(h hash.Hash) error {
	return returnErr(p.client.FetchObjectRequest(h, TypeTree))
}
func (p *Provider) FetchObject(h hash.Hash) error {
	return returnErr(p.client.FetchObjectRequest(h, TypeObject))
}
func (p *Provider) Accessed(h hash.Hash) error {
	return p.client.SendNotifyUse(h)
}
func (p *Provider) NewFile(h hash.Hash) error {
	return p.client.SendNewObject(h, TypeBlob)
}
func (p *Provider) NewSymLink(h hash.Hash) error {
	return p.client.SendNewObject(h, TypeBlob)
}
func (p *Provider) NewDirEntries(h hash.Hash) error {
	return p.client.SendNewObject(h, TypeTree)
}
func (p *Provider) NewDirList(h hash.Hash) error {
	// We don't persist these
	return nil
}
func (p *Provider) NewDeepTree(h hash.Hash) error {
	return p.client.SendNewObject(h, TypeTree)
}
