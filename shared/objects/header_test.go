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

package objects

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPutHeaderEmpty(t *testing.T) {
	content := make([]byte, MaxHeaderSize)
	content, err := PutHeader("empty", content)
	require.NoError(t, err)
	require.Equal(t, string(content), "empty 0\x00")
}

func TestPutHeaderTooShortSimple(t *testing.T) {
	content := make([]byte, MaxHeaderSize)
	body := []byte("hello")
	content = append(content, body...)
	content, err := PutHeader("simple", content)
	require.NoError(t, err)
	require.Equal(t, string(content), "simple 5\x00hello")
}

func TestPutHeaderTooLongType(t *testing.T) {
	content := make([]byte, MaxHeaderSize)
	_, err := PutHeader("too-long-type/too-long-type/too-long-type/too-long-type/too-long-type/too-long-type/too-long-type/too-long-type", content)
	require.Error(t, err)
}

func TestPutHeaderTooShortContent(t *testing.T) {
	content := make([]byte, 10)
	_, err := PutHeader("too-short", content)
	require.Error(t, err)
}
