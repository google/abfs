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
	"context"
	"errors"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
)

const defaultWaitHTTPMargin = 5 * time.Second

var errNotTried = errors.New("not tried yet")

type waitCmd struct {
	*cachemanCmd
	*cmd.CachemanProvider
	timeout    int64
	noRetry    bool
	httpMargin time.Duration
}

func (c *waitCmd) margin() time.Duration {
	if c.httpMargin > 0 {
		return c.httpMargin
	}
	return defaultWaitHTTPMargin
}

func NewWaitCmd(c *cachemanCmd) *waitCmd {
	return &waitCmd{cachemanCmd: c}
}

func (c *waitCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "wait",
		Usage:   "[-t <timeout in seconds>]",
		Aliases: []string{},
		Desc:    `Waits for cacheman dagsync to be idle.`,
	}
}

func (c *waitCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.Int64VarP(&c.timeout, "timeout", "t", 10, "Number of seconds before timing out")
	fl.BoolVarP(&c.noRetry, "no-retry", "n", false, "Do not retry if cacheman is not available")
}

func (c *waitCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	if c.timeout <= 0 {
		return clierr.CLIErr("timeout must be positive")
	}

	return c.run(ctx)
}

func (c *waitCmd) run(ctx context.Context) error {
	endTime := time.Now().Add(time.Duration(c.timeout) * time.Second)

	for err := errNotTried; err != nil; {
		timeout := time.Until(endTime)
		if timeout < 0 || (c.noRetry && err != errNotTried) {
			return err
		}
		// Leave margin beyond the server wait timeout so a responsive cacheman returns
		// its own 408 error, while a hung socket still fails instead of blocking forever.
		reqCtx, cancel := context.WithDeadline(ctx, endTime.Add(c.margin()))
		err = c.Cacheman.WaitIdleContext(reqCtx, timeout)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if c.noRetry || time.Until(endTime) <= 0 {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1 * time.Second):
			}
		}
	}

	return nil
}
