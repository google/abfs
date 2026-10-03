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

package buildmanbuilder

import (
	"context"
	"fmt"
	"log"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/google/abfs/buildman/builder"
	"github.com/google/abfs/shared/entities"
)

type gcpPubSubRequestReceiver struct {
	sub *pubsub.Subscriber
}

func newGcpPubSubRequestReceiver(
	ctx context.Context,
	project,
	subscription string,
	maxConcurrentMessages int,
	maxMessageProcessTime time.Duration) (*gcpPubSubRequestReceiver, error) {

	client, err := pubsub.NewClient(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("failed to create pubsub client for project %q: %w", project, err)
	}

	sub := client.Subscriber(subscription)
	sub.ReceiveSettings.MaxOutstandingMessages = maxConcurrentMessages
	// for some reason, this is separate from MaxOutstandingMessages, and if NumGoroutines is higher
	// then this subscriber can pull messages when it's not ready to work on them.
	sub.ReceiveSettings.NumGoroutines = maxConcurrentMessages
	// the library will automatically extend our deadline up to this maximum until we ack or nack
	sub.ReceiveSettings.MaxExtension = maxMessageProcessTime

	return &gcpPubSubRequestReceiver{sub}, nil
}

var _ builder.Receiver = (*gcpPubSubRequestReceiver)(nil)

func (p *gcpPubSubRequestReceiver) Receive(ctx context.Context, f func(context.Context, entities.Hash) error) error {
	return p.sub.Receive(ctx, func(ctx context.Context, m *pubsub.Message) {
		messageResponseSent := false
		defer func() {
			// panics aren't handled by the pubsub library
			if err := recover(); err != nil {
				log.Printf("ERROR: panic occurred from message ID %q: %v", m.ID, err)
			}
			// if this function never sends an ack or nack, the server will send it to another receiver after some timeout
			if !messageResponseSent {
				log.Printf("ERROR: message ID %q was never acked or nacked, so we are nacking by default", m.ID)
				m.Nack()
			}
		}()
		hashStr := string(m.Data)
		if !plumbing.IsHash(hashStr) {
			log.Printf("ERROR: string in pubsub message ID %q with data %q is not a valid hash", m.ID, hashStr)
			// TODO: this is an invalid hash, so a retry will also fail. We should send to dead letter queue and Ack the message immediately
			m.Nack()
			messageResponseSent = true
			return
		}
		h := entities.NewHash(hashStr)
		if err := f(ctx, h); err != nil {
			m.Nack()
			messageResponseSent = true
			return
		}
		m.Ack()
		messageResponseSent = true
	})
}
