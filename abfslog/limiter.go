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
	"fmt"
	"log"
	"sync"
	"time"
)

const (
	limiterLines  = 20
	limiterWindow = time.Minute
)

// Limiter logs at most limiterLines lines per limiterWindow. Keep one Limiter per kind of line for the
// whole process, so that many callers failing at once, as in a server outage, do not flood the log.
type Limiter struct {
	what      string
	now       func() time.Time
	afterFunc func(time.Duration, func())

	mu         sync.Mutex
	start      time.Time
	lines      int
	suppressed int
	first      string
	logger     *log.Logger
}

// NewLimiter creates a Limiter for lines of the given kind, which the count line names.
func NewLimiter(what string) *Limiter {
	return &Limiter{
		what:      what,
		now:       time.Now,
		afterFunc: func(d time.Duration, f func()) { time.AfterFunc(d, f) },
	}
}

// Printf logs a line unless the limit for the current window is used up. The first line left out in a
// window schedules one line with the count at the end of the window.
func (l *Limiter) Printf(logger *log.Logger, format string, v ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.start) >= limiterWindow {
		l.start = now
		l.lines = 0
	}
	if l.lines < limiterLines {
		l.lines++
		logger.Printf(format, v...)
		return
	}
	if l.suppressed == 0 {
		l.afterFunc(l.start.Add(limiterWindow).Sub(now), l.Flush)
		l.first = fmt.Sprintf(format, v...)
		l.logger = logger
	}
	l.suppressed++
}

// Flush writes the count of the lines left out so far, if any.
func (l *Limiter) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.suppressed > 0 {
		l.logger.Printf("Suppressed %d %s lines, at most %d are logged per %s, first: %s", l.suppressed, l.what, limiterLines, limiterWindow, l.first)
		l.suppressed = 0
		l.first = ""
		l.logger = nil
	}
}
