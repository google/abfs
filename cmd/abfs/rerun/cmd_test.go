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

package rerun

import (
	"bytes"
	"testing"

	"github.com/google/abfs/shared/entities"

	"github.com/stretchr/testify/assert"
)

func TestWritePatchDiff(t *testing.T) {
	a := entities.Patch{
		{
			Path: "a/b/c/d",
			Mode: entities.FileModeDir,
			Hash: entities.NewHash("a16d8cf66e84235cdf32182cb45b9b00bb55cfd7"),
		},
		{
			Path: "a/b/e",
			Mode: entities.FileModeSymlink,
			Hash: entities.NewHash("3b01b066d0257aeaa1b80e34b203c26398f0a910"),
		},
		{
			Path: "a/f/g",
			Mode: entities.FileModeRegular,
			Hash: entities.NewHash("a92970d8c6818000b21ece0add1cace9af7a1eb1"),
		},
	}

	b := entities.Patch{
		{
			Path: "a/b/c/d",
			Mode: entities.FileModeDir,
			Hash: entities.NewHash("a16d8cf66e84235cdf32182cb45b9b00bb55cfd7"),
		},
		{
			Path: "a/b/e",
			Mode: entities.FileModeSymlink,
			Hash: entities.NewHash("ac7d6b4bf7a406e3fe06d4e9a24db413f4f6ef35"),
		},
		{
			Path: "a/b/c",
			Mode: entities.FileModeRegular,
			Hash: entities.NewHash("a92970d8c6818000b21ece0add1cace9af7a1eb1"),
		},
	}

	w := bytes.NewBuffer(nil)
	c := &rerunCmd{
		out: w,
	}
	c.writePatchDiff("test", a, b)

	assert.Regexp(t, `-\s*unassigned\s*3b01b066d0257aeaa1b80e34b203c26398f0a910 120000 a/b/e`, w.String())
	assert.Regexp(t, `\+\s*unassigned\s*ac7d6b4bf7a406e3fe06d4e9a24db413f4f6ef35 120000 a/b/e`, w.String())
	assert.Regexp(t, `-\s*unassigned\s*a92970d8c6818000b21ece0add1cace9af7a1eb1 100644 a/f/g`, w.String())
	assert.Regexp(t, `\+\s*unassigned\s*a92970d8c6818000b21ece0add1cace9af7a1eb1 100644 a/b/c`, w.String())
}

func TestWriteListDiff(t *testing.T) {
	a := []string{
		"a/b/c/d",
		"a/b/e",
		"a/f/g",
	}

	b := []string{
		"a/b/c/d",
		"a/b/e",
		"a/b/c",
	}

	w := bytes.NewBuffer(nil)
	c := &rerunCmd{
		out: w,
	}
	c.writeListDiff("test", a, b)

	expected := `test MISMATCH:
  - a/f/g
  + a/b/c
`
	assert.Equal(t, expected, w.String())
}
