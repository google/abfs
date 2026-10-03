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
	"fmt"
	"os"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
)

type remoteOkCmd struct {
	*cachemanCmd
	*cmd.CachemanProvider
	timeout time.Duration
}

func NewRemoteOkCmd(c *cachemanCmd) *remoteOkCmd {
	return &remoteOkCmd{cachemanCmd: c}
}

func (c *remoteOkCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "remote-ok",
		Usage: "[-t <timeout in seconds>]",
		Desc:  `Check if cacheman's remote connection is healthy.`,
	}
}

func (c *remoteOkCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.DurationVarP(&c.timeout, "timeout", "t", 10*time.Second, "Timeout duration")
}

func (c *remoteOkCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return c.remoteOk(ctx)
}

func (c *remoteOkCmd) remoteOk(ctx context.Context) error {
	timeoutTime := time.Now().Add(c.timeout)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		if err := c.Cacheman.RemoteOK(); err == nil {
			return nil
		} else {
			fmt.Fprintf(os.Stderr, "Remote check failed: %v, retrying...\n", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case t := <-ticker.C:
			if t.Compare(timeoutTime) == 1 {
				return errors.New("timeout waiting for a successful remote check")
			}
		}
	}
}
