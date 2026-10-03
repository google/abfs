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
	"runtime"
	"sync"

	"golang.org/x/sync/errgroup"
)

// BreakGroup is similar to sync.errgroup.Group but it will not continue running the functions once
// an error is returned.
type BreakGroup struct {
	group    *errgroup.Group
	errOnce  sync.Once
	err      error
	hasError bool
}

func New() *BreakGroup {
	group := new(errgroup.Group)
	group.SetLimit(runtime.NumCPU())
	return &BreakGroup{group: group}
}

func (g *BreakGroup) SetLimit(n int) {
	g.group.SetLimit(n)
}

func (g *BreakGroup) Go(f func() error) {
	g.group.Go(
		func() error {
			if g.hasError {
				return g.err
			}
			err := f()
			if err != nil {
				g.errOnce.Do(func() {
					g.err = err
					g.hasError = true
				})
			}
			return err
		},
	)
}

func (g *BreakGroup) Wait() error {
	return g.group.Wait()
}
