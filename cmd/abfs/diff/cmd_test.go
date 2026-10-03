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

package diff

import (
	"bytes"
	"testing"

	"github.com/google/abfs/shared/entities"

	"github.com/stretchr/testify/assert"
)

func TestDiffPatch(t *testing.T) {
	diffBuf := bytes.NewBuffer(nil)
	c := &diffCmd{
		out: diffBuf,
	}

	p1 := entities.Patch{
		{
			Path: "a/b/c",
			Mode: 1100755,
			Hash: entities.NewHash("0f828508daa94bc9ff69854d07d1a808bdec1e8b"),
			Size: 1234,
		},
		{
			Path: "a/b/c1",
			Mode: 1100644,
			Hash: entities.NewHash("3e5f50e347901a009ba7030717f386fa7ab6064a"),
			Size: 4202,
		},
		{
			Path: "a/b2/c",
			Mode: 1100755,
			Hash: entities.NewHash("e3bf148b9845b07767f1a73da690202f0ba63639"),
			Size: 4300,
		},
	}
	c.diffPatch(p1, p1)
	assert.Zero(t, diffBuf.String())

	p2 := entities.Patch{
		{
			Path: "a/b/c1",
			Mode: 1100644,
			Hash: entities.NewHash("3e5f50e347901a009ba7030717f386fa7ab6064a"),
			Size: 4202,
		},
		{
			Path: "a/b2/c",
			Mode: 1100755,
			Hash: entities.NewHash("e3bf148b9845b07767f1a73da690202f0ba63639"),
			Size: 4300,
		},
		{
			Path: "a/b3/c",
			Mode: 1100755,
			Hash: entities.NewHash("a737572522899a7253cc0310e014393d0d7394b4"),
			Size: 639,
		}}
	c.diffPatch(p1, p2)
	expected := `- 4145723 0f828508daa94bc9ff69854d07d1a808bdec1e8b         1234 a/b/c
+ 4145723 a737572522899a7253cc0310e014393d0d7394b4          639 a/b3/c
`
	assert.Equal(t, expected, diffBuf.String())
}
