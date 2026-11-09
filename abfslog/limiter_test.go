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

package abfslog

import (
	"log"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeTimer struct {
	now     time.Time
	after   []time.Duration
	flushes []func()
}

func newTestLimiter(timer *fakeTimer) *Limiter {
	l := NewLimiter("failed push")
	l.now = func() time.Time { return timer.now }
	l.afterFunc = func(d time.Duration, f func()) {
		timer.after = append(timer.after, d)
		timer.flushes = append(timer.flushes, f)
	}
	return l
}

func TestLimiter_LimitsLinesPerWindow(t *testing.T) {
	timer := &fakeTimer{now: time.Unix(1000, 0)}
	l := newTestLimiter(timer)
	var out strings.Builder
	logger := log.New(&out, "", 0)
	lines := func() []string { return strings.Split(strings.TrimSpace(out.String()), "\n") }

	// An outage: many nodes fail within one window.
	for i := range 1000 {
		l.Printf(logger, "Failed to push %d", i)
		timer.now = timer.now.Add(10 * time.Millisecond)
	}
	require.Len(t, lines(), limiterLines)
	require.Len(t, timer.flushes, 1)
	require.Equal(t, limiterWindow-time.Duration(limiterLines)*10*time.Millisecond, timer.after[0])

	// The end of the window writes one line with the count and the first line left out.
	timer.flushes[0]()
	require.Len(t, lines(), limiterLines+1)
	require.Equal(t, "Suppressed 980 failed push lines, at most 20 are logged per 1m0s, first: Failed to push 20", lines()[limiterLines])

	// The next window logs again.
	timer.now = timer.now.Add(limiterWindow)
	l.Printf(logger, "Failed to push y")
	require.Len(t, lines(), limiterLines+2)
	require.Equal(t, "Failed to push y", lines()[limiterLines+1])
}

// A daemon that stops calls Flush, so the count is not lost with the pending timer.
func TestLimiter_FlushWritesCountAtOnce(t *testing.T) {
	timer := &fakeTimer{now: time.Unix(1000, 0)}
	l := newTestLimiter(timer)
	var out strings.Builder
	logger := log.New(&out, "", 0)

	for i := range limiterLines + 3 {
		l.Printf(logger, "Failed to push %d", i)
	}
	l.Flush()
	require.True(t, strings.HasSuffix(out.String(), "Suppressed 3 failed push lines, at most 20 are logged per 1m0s, first: Failed to push 20\n"), out.String())

	// The timer of the window finds nothing left to write.
	before := out.String()
	timer.flushes[0]()
	require.Equal(t, before, out.String())
}
