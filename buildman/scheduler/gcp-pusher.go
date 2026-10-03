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

package scheduler

import (
	"context"
	"fmt"

	"cloud.google.com/go/pubsub/v2"

	"github.com/google/abfs/shared/entities"
)

type GCPPusher struct {
	publisher *pubsub.Publisher
}

func NewGCPPusher(ctx context.Context, project, topic string) (*GCPPusher, error) {
	c, err := pubsub.NewClient(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("failed to create pubsub client for project %q: %v", project, err)
	}
	return &GCPPusher{
		publisher: c.Publisher(topic),
	}, nil
}

func (p *GCPPusher) Close() error {
	p.publisher.Stop()
	return nil
}

// Push pushes synchronously. We could make an asynchronous version without calling
// `topic.Publish.Get()` if we wanted.
func (p *GCPPusher) Push(ctx context.Context, h entities.Hash) error {
	_, err := p.publisher.Publish(ctx, &pubsub.Message{
		Data: []byte(h.String()),
	}).Get(ctx)
	return err
}
