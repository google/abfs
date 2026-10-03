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

package main

import (
	"log"

	"go.coder.com/cli"

	"github.com/google/abfs/cas/cmd/deeptree"
	"github.com/google/abfs/cas/cmd/diff"
	"github.com/google/abfs/cas/cmd/hashes"
	"github.com/google/abfs/cas/cmd/inspect"
	"github.com/google/abfs/cas/cmd/resolve"
	"github.com/google/abfs/cas/cmd/root"
)

func main() {
	log.SetFlags(0)
	r := root.NewRootCommand()
	r.Commands = []cli.Command{
		inspect.NewCmd(r),
		diff.NewCmd(r),
		resolve.NewCmd(r),
		hashes.NewCmd(r),
		deeptree.NewCmd(r),
	}

	cli.RunRoot(r)
	r.Cleanup()
}
