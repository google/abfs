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

package main

import (
	"fmt"
	"log"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/shared/bindcas"
)

type HashCmd struct {
}

func NewHashCmd() *HashCmd {
	return &HashCmd{}
}

func (c *HashCmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:    "hash",
		Usage:   "<file>",
		Aliases: []string{},
		Desc:    `Read file hash.`,
		RawArgs: true,
	}
}

func (c *HashCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *HashCmd) Run(fl *pflag.FlagSet) {
	args := fl.Args()
	if len(args) != 1 {
		log.Fatal("Expecting a single <file> argument.")
	}
	hash, err := bindcas.Hash(args[0])
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(hash)
}
