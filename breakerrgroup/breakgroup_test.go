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

package breakerrgroup

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoError(t *testing.T) {
	var lock sync.Mutex

	g := New()
	g.SetLimit(2)

	const n = 10
	var cc int32

	for i := 0; i < n; i++ {
		g.Go(
			func() error {
				lock.Lock()
				defer lock.Unlock()
				atomic.AddInt32(&cc, 1)
				time.Sleep(time.Millisecond)
				return nil
			},
		)
	}

	require.NoError(t, g.Wait())
	assert.Equal(t, n, int(cc))
}

func TestWithError(t *testing.T) {
	var lock sync.Mutex

	g := New()
	g.SetLimit(2)

	const n = 10
	var cc int32

	for i := range n {
		g.Go(
			func() error {
				lock.Lock()
				defer lock.Unlock()
				if i > 5 {
					return errors.New("some error")
				}
				atomic.AddInt32(&cc, 1)
				time.Sleep(time.Millisecond)
				return nil
			},
		)
	}

	require.Error(t, g.Wait())
	assert.Greater(t, n, int(cc))
}
