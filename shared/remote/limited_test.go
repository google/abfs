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

package remote

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type blockingRemote struct {
	RemoteCloser
	started chan struct{}
	release chan struct{}
}

func (r *blockingRemote) Version(ctx context.Context) (string, error) {
	r.started <- struct{}{}
	<-r.release
	return "v", nil
}

func TestLimitedRemoteProvider_WaitTime(t *testing.T) {
	require := require.New(t)
	c := &blockingRemote{started: make(chan struct{}, 2), release: make(chan struct{})}
	r := NewLimitedRemote(c, 1)

	go func() { _, _ = r.Version(context.Background()) }()
	<-c.started
	require.Zero(r.WaitTime(), "a call that gets a token at once does not wait")

	done := make(chan error)
	go func() {
		_, err := r.Version(context.Background())
		done <- err
	}()
	const hold = 50 * time.Millisecond
	time.Sleep(hold)
	c.release <- struct{}{}
	<-c.started
	c.release <- struct{}{}
	require.NoError(<-done)
	require.GreaterOrEqual(r.WaitTime(), hold)
}

func TestLimitedRemoteProvider_WaitTimeOnCancel(t *testing.T) {
	require := require.New(t)
	c := &blockingRemote{started: make(chan struct{}, 1), release: make(chan struct{})}
	r := NewLimitedRemote(c, 1)
	go func() { _, _ = r.Version(context.Background()) }()
	<-c.started
	defer close(c.release)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := r.Version(ctx)
	require.ErrorIs(err, context.DeadlineExceeded)
	require.GreaterOrEqual(r.WaitTime(), 20*time.Millisecond)
}
