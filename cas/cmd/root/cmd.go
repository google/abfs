// Copyright 2025 Google LLC
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

package root

import (
	"log"
	"path/filepath"
	"time"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/cas/oms"
	"github.com/google/abfs/cas/cas/providers"
	"github.com/google/abfs/cas/cmd/args"
	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/cas/hash/hashers"
)

// Only one hasher currently supported
const hasherType = hashers.HasherTypeSHA1

// Only one object model currently supported
const objectModelType = oms.ObjectModelTypeGIT

// Only one provider type
const providerType = providers.ProviderTypeCacheman

type RootCommand struct {
	Commands     []cli.Command
	flagSet      *pflag.FlagSet
	Hasher       hash.Hasher
	CacheDir     string
	Local        bool
	FetchTimeout time.Duration
	store        cas.Store
}

func NewRootCommand() *RootCommand {
	return &RootCommand{
		Hasher: hashers.CreateHasher(hasherType),
	}
}
func (c *RootCommand) HasherDescription() string {
	return hasherType.String()
}

func (c *RootCommand) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVar(&c.CacheDir, "cache", "", "Path to cache dir")
	fl.BoolVarP(&c.Local, "local", "l", false, "Run without remote support")
	fl.DurationVar(&c.FetchTimeout, "fetch-timeout", 5*time.Second, "The timeout for fetching remote objects")
}
func (c *RootCommand) Run(fl *pflag.FlagSet) {
	fl.Usage()
}

func (c *RootCommand) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:  "cas",
		Usage: "<command>",
		Desc:  `Command line interface for cas`,
	}
}
func (c *RootCommand) Subcommands() []cli.Command {
	return c.Commands
}

func (c *RootCommand) CreateHash(h string) (hash.Hash, error) {
	return c.Hasher.NewHash(h)
}
func (c *RootCommand) Store() (cas.Store, error) {
	if err := c.createStore(); err != nil {
		return nil, err
	}
	return c.store, nil
}
func (c *RootCommand) createStore() error {
	if c.store != nil {
		return nil
	}
	// For current cli for CAS we don't need an exclusive scratch directory
	store := oms.CreateObjectModel(objectModelType, c.Hasher, c.CacheDir, "cas", false)
	var p cas.Provider
	if !c.Local {
		// Create provider
		endPt := filepath.Join(c.CacheDir, "socket")
		p = providers.CreateProvider(providerType, endPt, c.FetchTimeout)
		store = cas.NewRemote(store, p)
	}
	err := store.Open()
	if err != nil {
		return err
	}

	c.store = store
	return nil
}
func (c *RootCommand) HashAndStoreFromSingleArg(fl *pflag.FlagSet) (hash.Hash, cas.Store) {
	hashStr := args.EnsureSingleArg(fl)
	h, err := c.CreateHash(hashStr)
	if err != nil {
		log.Fatal(err)
	}

	store, err := c.Store()
	if err != nil {
		log.Fatal(err)
	}

	return h, store

}
func (c *RootCommand) Cleanup() {
	if c.store != nil {
		c.store.Close()
	}
}
