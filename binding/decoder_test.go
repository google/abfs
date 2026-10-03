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

package binding

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/objects/gitobjects"
)

func newEmpty(content []byte) (gitobjects.ObjectType, objects.Object, error) {
	return objects.Factory.NewEmpty(entities.ZeroHash, content)
}

func TestNewEmptyObject(t *testing.T) {
	require := require.New(t)

	ot, o, err := newEmpty([]byte("commit 0\x00"))
	require.NoError(err)
	require.Equal(gitobjects.ObjectTypeCommit, ot)
	_, ok := o.(*gitobjects.Commit)
	require.True(ok, "type check")

	ot, o, err = newEmpty([]byte("tree 0\x00"))
	require.NoError(err)
	require.Equal(gitobjects.ObjectTypeDir, ot)
	_, ok = o.(*gitobjects.Tree)
	require.True(ok, "type check")

	ot, o, err = newEmpty([]byte("env-vars 0\x00"))
	require.NoError(err)
	require.Equal(ObjectTypeEnvVars, ot)
	_, ok = o.(*EnvVars)
	require.True(ok, "type check")

	ot, o, err = newEmpty([]byte("eval-request 0\x00"))
	require.NoError(err)
	require.Equal(ObjectTypeEvalRequest, ot)
	_, ok = o.(*EvalRequest)
	require.True(ok, "type check")

	ot, o, err = newEmpty([]byte("eval-response 0\x00"))
	require.NoError(err)
	require.Equal(ObjectTypeEvalResponse, ot)
	_, ok = o.(*EvalResponse)
	require.True(ok, "type check")

	ot, o, err = newEmpty([]byte("patch 0\x00"))
	require.NoError(err)
	require.Equal(ObjectTypePatch, ot)
	_, ok = o.(*PatchObject)
	require.True(ok, "type check")
}

func TestDecodeObject_EmptyDir(t *testing.T) {
	require := require.New(t)

	ot, o, err := objects.Factory.Decode(entities.EmptyDirHash, entities.EmptyDirContent)
	require.NoError(err)
	require.Equal(gitobjects.ObjectTypeDir, ot)
	tree, ok := o.(*gitobjects.Tree)
	require.True(ok, "type check")

	require.Empty(tree.GitTree().Entries)
}

// TestOldEvalReqDecode ensures that the old 0.8.1 format of eval requests can be decoded by the server once
// 0.9.0 is introduced that deprecates the `Inputs` field. This test can be deleted when the `Inputs` field is deleted.
func TestOldEvalReqDecode(t *testing.T) {
	require := require.New(t)
	req := &EvalRequest{
		Version: "0.8.1",
		// if Inputs is deleted, delete this test also
		Inputs: map[string]entities.Hash{
			"fakepath": entities.EmptyFileHash,
		},
	}
	require.NoError(req.Encode())

	ty, obj, err := objects.Factory.Decode(req.Hash, req.Encoded)
	require.NoError(err)
	require.Equal(ObjectTypeEvalRequest, ty)
	_, ok := obj.(*EvalRequest)
	require.True(ok)
}
