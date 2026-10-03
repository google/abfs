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
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/spf13/pflag"

	"github.com/google/abfs/cas/cas/providers/cacheman"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/entities"
)

type clientCmd struct {
	*cachemanCmd
	*cmd.CachemanProvider
}

func NewClientCmd(c *cachemanCmd) *clientCmd {
	return &clientCmd{cachemanCmd: c}
}

func (c *clientCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "client",
		Usage: "",
		Desc:  `Notifies cacheman to push or pull objects.`,
	}
}

func (c *clientCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *clientCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	if err := c.Cacheman.Serve(ctx); err != nil {
		return err
	}

	return c.serve(os.Stdin, os.Stdout)
}

var typeStringMap = map[string]cacheman.RequestObjectType{
	"blob":   cacheman.TypeBlob,
	"tree":   cacheman.TypeTree,
	"object": cacheman.TypeObject,
}

func (c *clientCmd) serve(r io.Reader, w io.Writer) error {
	config := c.Config
	var wait sync.WaitGroup
	defer wait.Wait()

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		f := strings.Fields(scanner.Text())
		if len(f) < 3 {
			return fmt.Errorf("invalid number of fields in %q", scanner.Text())
		}
		if err := checkChoices(f[0], "first field", "fetch", "push"); err != nil {
			return err
		}
		if err := checkChoices(f[1], "second field", "blob", "tree", "object", "map"); err != nil {
			return err
		}

		var err error

		switch f[0] {
		case "fetch":
			var result cacheman.ResultType
			hash := entities.NewHash(f[2])
			switch f[1] {
			case "blob", "tree", "object":
				result, err = c.Cacheman.FetchObjectRequest(hash, typeStringMap[f[1]])
			case "map":
				err = c.Cacheman.FetchMapRequest(hash)
			}
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "Fetch %s result=%s\n", hash, result); err != nil {
				return err
			}
		case "push":
			switch f[1] {
			case "blob", "tree", "object":
				err = c.Cacheman.SendNewObject(entities.NewHash(f[2]), typeStringMap[f[1]])
			case "map":
				err = c.Cacheman.SendNewMap(entities.NewHash(f[2]), entities.NewHash(f[3]), config.LocalOnly)
			}
			if err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func checkChoices(input, name string, choices ...string) error {
	if slices.Index(choices, input) < 0 {
		return fmt.Errorf("%s can be only one of the following: %s", name, strings.Join(choices, " "))
	}
	return nil
}
