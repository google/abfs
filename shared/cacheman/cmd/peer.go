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

package cacheman

import (
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/instance"
)

var (
	// cachemanAnswerTimeout bounds each connect and request to cacheman, so a stuck
	// cacheman cannot hang the command.
	cachemanAnswerTimeout = 5 * time.Second
	// A unix connect fails at once with EAGAIN while the listen queue is full, which a
	// burst of clients can cause for a moment.
	listenQueueRetries = 10
	listenQueueWait    = 100 * time.Millisecond
)

var errListenQueueFull = errors.New("listen queue is full")

// findCacheman returns the process that listens on the cacheman socket of inst.
// found is false when nothing listens there.
func findCacheman(inst *instance.Instance) (peer client.Peer, found bool, err error) {
	cacheDir := inst.Config.CacheDir
	for i := 0; ; i++ {
		peer, err = client.DialPeer(cacheDir, cachemanAnswerTimeout)
		switch {
		case err == nil:
			return peer, true, nil
		case errors.Is(err, syscall.ENOENT), errors.Is(err, syscall.ECONNREFUSED):
			return client.Peer{}, false, nil
		case errors.Is(err, syscall.EAGAIN) && i < listenQueueRetries:
			time.Sleep(listenQueueWait)
		case errors.Is(err, syscall.EACCES):
			return client.Peer{}, false, fmt.Errorf("permission denied on %s, so cacheman may run as another user: %w", client.HTTPSocketPath(cacheDir), err)
		case errors.Is(err, syscall.EAGAIN):
			return client.Peer{}, true, fmt.Errorf("cacheman listens on %s, but its %w", client.HTTPSocketPath(cacheDir), errListenQueueFull)
		default:
			return client.Peer{}, false, fmt.Errorf("failed to connect to cacheman on %s: %w", client.HTTPSocketPath(cacheDir), err)
		}
	}
}
