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

	"github.com/google/abfs/shared/remote"
)

func SimpleMasterDepot() *MasterDepot {
	return NewMasterDepot([]Depot{
		NewMemoryDepot("mem-1", 10),
		NewMemoryDepot("mem-2", 20),
	})
}

func TestMasterEmpty(t *testing.T) {
	ctx := context.Background()

	master := SimpleMasterDepot()

	ss, err := master.Store(ctx, "test-obj-uuid", strings.NewReader(""))
	require.NoError(t, err)
	assert.Equal(t, "[]", ss)

	r, err := master.Load(ctx, ss)
	require.NoError(t, err)
	buf, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Empty(t, buf)
}

func TestMasterLongLine(t *testing.T) {
	ctx := context.Background()

	master := SimpleMasterDepot()

	longLine := "hello world! how are you doing? this is a looooong line!"
	ss, err := master.StoreChunks(ctx, "test-obj-uuid", strings.NewReader(longLine))
	require.NoError(t, err)
	require.Len(t, ss, 4)
	assert.Equal(t, "mem-1", ss[0].Depot)
	assert.Equal(t, int64(10), ss[0].Size)
	assert.Equal(t, "mem-2", ss[1].Depot)
	assert.Equal(t, int64(20), ss[1].Size)
	assert.Equal(t, "mem-2", ss[2].Depot)
	assert.Equal(t, int64(20), ss[2].Size)
	assert.Equal(t, "mem-2", ss[3].Depot)
	assert.Equal(t, int64(6), ss[3].Size)

	r, err := master.LoadChunks(ctx, ss)
	require.NoError(t, err)
	buf, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, longLine, string(buf))

	mem1 := master.depots[0].(*MemoryDepot)
	mem2 := master.depots[1].(*MemoryDepot)

	assert.Equal(t, 1, mem1.count())
	assert.Equal(t, 3, mem2.count())

	err = master.RemoveChunks(ctx, ss)
	require.NoError(t, err)

	assert.Equal(t, 0, mem1.count())
	assert.Equal(t, 0, mem2.count())
}

func TestMasterAlignedLine(t *testing.T) {
	ctx := context.Background()

	master := SimpleMasterDepot()

	alignedLine := "123456789012345678901234567890"
	ss, err := master.StoreChunks(ctx, "test-obj-uuid", strings.NewReader(alignedLine))
	require.NoError(t, err)
	require.Len(t, ss, 2)
	assert.Equal(t, "mem-1", ss[0].Depot)
	assert.Equal(t, int64(10), ss[0].Size)
	assert.Equal(t, "mem-2", ss[1].Depot)
	assert.Equal(t, int64(20), ss[1].Size)

	r, err := master.LoadChunks(ctx, ss)
	require.NoError(t, err)
	buf, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, alignedLine, string(buf))
}

func TestMasterWithIgnoreTooBig(t *testing.T) {
	ctx := context.Background()

	master := NewMasterDepot([]Depot{
		NewInlineDepot("inline-1", 4, true),
		NewInlineDepot("inline-2", 4, false),
	})

	ss, err := master.StoreChunks(ctx, "test-obj-uuid", strings.NewReader("12345678901234567890"))
	require.NoError(t, err)
	require.Len(t, ss, 5)
	assert.Equal(t, "inline-2", ss[0].Depot)
	assert.Equal(t, "MTIzNA==", ss[0].ID) // "1234"
	assert.Equal(t, "inline-2", ss[1].Depot)
	assert.Equal(t, "NTY3OA==", ss[1].ID) // "5678"

	ss, err = master.StoreChunks(ctx, "test-obj-uuid", strings.NewReader("123"))
	require.NoError(t, err)
	require.Len(t, ss, 1)
	assert.Equal(t, "inline-1", ss[0].Depot)
	assert.Equal(t, "MTIz", ss[0].ID) // "123"
}

type noLinksDepot struct {
	Depot
}

func (n *noLinksDepot) LinkedContent(ctx context.Context, s Chunk) (remote.ContentLink, error) {
	return remote.ContentLink{}, ErrNoLinkedContent
}

func TestMasterLinkedContents(t *testing.T) {
	ctx := context.Background()
	master := SimpleMasterDepot()

	ss, err := master.StoreChunks(ctx, "test-obj-uuid", strings.NewReader("hello"))
	require.NoError(t, err)

	links, err := master.LinkedContentsChunks(ctx, ss)
	require.NoError(t, err)
	require.Len(t, links, 1)
	assert.Equal(t, remote.LinkTypeBase64, links[0].LinkType)
	assert.Equal(t, "aGVsbG8=", links[0].Content)
}

func TestMasterNoLinkedContent(t *testing.T) {
	ctx := context.Background()
	mem := NewMemoryDepot("mem-nolink", 100)
	noLink := &noLinksDepot{Depot: mem}
	master := NewMasterDepot([]Depot{noLink})

	ss, err := master.StoreChunks(ctx, "test-obj-uuid", strings.NewReader("hello"))
	require.NoError(t, err)

	_, err = master.LinkedContentsChunks(ctx, ss)
	assert.ErrorIs(t, err, ErrNoLinkedContent)
}
