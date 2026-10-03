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

package push

import (
	"fmt"
	"testing"

	shellgit "github.com/google/abfs/git"
	"github.com/google/abfs/shared/entities"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/require"
)

type fakeRunner struct {
}

func (f *fakeRunner) ObjCount(dir, rev string, excludes []entities.Hash) (int64, error) {
	return 10, nil
}

func (f *fakeRunner) RevListObjectsTopoOrder(dir, rev string, excludes []entities.Hash) (<-chan shellgit.ObjectTypeNameStreamItem, error) {

	out := make(chan shellgit.ObjectTypeNameStreamItem)
	go func() {
		defer func() {
			close(out)
		}()

		unordered := []*shellgit.ObjectTypeName{
			{ObjType: plumbing.CommitObject, Name: entities.NewHash("ebb21d79fbb4c84e50dc3c4f94f87fe1ec8ad0bc")},
			{ObjType: plumbing.TreeObject, Name: entities.NewHash("b4d01e9b0c4a9356736dfddf8830ba9a54f5271c")},
			{ObjType: plumbing.BlobObject, Name: entities.NewHash("ce013625030ba8dba906f756967f9e9ca394464a")},
			{ObjType: plumbing.CommitObject, Name: entities.NewHash("de497b0c47a4f69d431008061bb9e19ea4cb05f8")},
			{ObjType: plumbing.TreeObject, Name: entities.NewHash("08001c511063db8c50650e5dd9b3fae06c107b54")},
			{ObjType: plumbing.BlobObject, Name: entities.NewHash("b023018cabc396e7692c70bbf5784a93d3f738ab")},
			{ObjType: plumbing.CommitObject, Name: entities.NewHash("b30dfb6ba65ec6d7b74ba639a424d27236ada9a5")},
			{ObjType: plumbing.TreeObject, Name: entities.NewHash("11943ab05848a5c82d0287fc89ce6d163a11b98d")},
			{ObjType: plumbing.TreeObject, Name: entities.NewHash("851c6b58228b7677f8fbbe090f7159d902fac435")},
			{ObjType: plumbing.BlobObject, Name: entities.NewHash("871bb87ecb3fb8381f98057ae8b691a64a62ead8")},
		}
		for _, obj := range unordered {
			out <- shellgit.ObjectTypeNameStreamItem{ObjectTypeName: obj, Err: nil}
		}
	}()

	return out, nil
}

func (f *fakeRunner) want() []string {
	return []string{
		"blob ce013625030ba8dba906f756967f9e9ca394464a",
		"tree b4d01e9b0c4a9356736dfddf8830ba9a54f5271c",
		"blob b023018cabc396e7692c70bbf5784a93d3f738ab",
		"tree 08001c511063db8c50650e5dd9b3fae06c107b54",
		"blob 871bb87ecb3fb8381f98057ae8b691a64a62ead8",
		"tree 851c6b58228b7677f8fbbe090f7159d902fac435",
		"tree 11943ab05848a5c82d0287fc89ce6d163a11b98d",
		"commit ebb21d79fbb4c84e50dc3c4f94f87fe1ec8ad0bc",
		"commit de497b0c47a4f69d431008061bb9e19ea4cb05f8",
		"commit b30dfb6ba65ec6d7b74ba639a424d27236ada9a5",
	}
}

func TestBadRepo(t *testing.T) {
	require := require.New(t)

	crawler := NewCrawler("", entities.ZeroHash, nil)
	_, err := crawler.Objects()
	require.Error(err)
}

func TestObjects(t *testing.T) {
	require := require.New(t)

	fakeRunner := &fakeRunner{}
	crawler := NewCrawler("/path", entities.ZeroHash, nil)
	crawler.Runner = fakeRunner

	ch, err := crawler.objects()
	require.NoError(err)

	var got []string
	for item := range ch {
		if item.Err != nil {
			require.Fail(fmt.Sprintf("%s", item.Err))
		}
		got = append(got, fmt.Sprintf("%v %v", item.ObjType, item.Name))
	}

	require.Equal(fakeRunner.want(), got)
}
