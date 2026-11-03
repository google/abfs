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

package depot

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInlineDepot(t *testing.T) {
	ctx := context.Background()
	d := NewInlineDepot("inline", 10, false)
	s, err := d.Store(ctx, "test-chunk-0", strings.NewReader("hello"))
	require.Equal(t, io.EOF, err)
	assert.Equal(t, Chunk{
		Size:  5,
		Depot: "inline",
		ID:    "aGVsbG8=",
	}, s)

	r, err := d.Load(ctx, s)
	require.NoError(t, err)
	buf, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(buf))
}
