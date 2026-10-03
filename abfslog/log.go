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

type TooLongLogger struct {
	wg     sync.WaitGroup
	ticker *time.Ticker
	done   chan struct{}
}

// NewLogIfTooLong will start a background goroutine that will log a message every interval
// if the duration since start is greater than interval. The returned struct should be used to
// stop the background goroutine.
func NewLogIfTooLong(interval time.Duration, format string, args ...any) *TooLongLogger {
	start := time.Now()
	ticker := time.NewTicker(interval)
	msg := fmt.Sprintf(format, args...)

	logger := &TooLongLogger{
		ticker: ticker,
		done:   make(chan struct{}),
	}

	logger.wg.Add(1)
	go func() {
		defer logger.wg.Done()
		for {
			select {
			case <-ticker.C:
				log.Printf("%s for %s", msg, time.Since(start))
			case <-logger.done:
				return
			}
		}
	}()
	return logger
}

// Stop will stop the background goroutine and wait for it to finish.
func (l *TooLongLogger) Stop() {
	l.ticker.Stop()
	close(l.done)
	l.wg.Wait()
}
