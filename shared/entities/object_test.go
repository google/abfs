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

package entities

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObjectSerialization(t *testing.T) {
	content := []byte("hello-world")

	buf := new(bytes.Buffer)
	err := WriteObject("test-object", content, buf)
	require.NoError(t, err)

	assert.Equal(t, "test-object 11\x00hello-world", buf.String())

	type_, content2, err := DecodeObject(buf.Bytes())
	require.NoError(t, err)
	assert.Equal(t, "test-object", type_)
	assert.Equal(t, content, content2)
}
