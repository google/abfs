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

package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/objects"
)

func TestCASStore_NotifyNewObjectError(t *testing.T) {
	// The provider never connects to cacheman, so every notify fails.
	c := &CASStore{provider: NewProvider(t.TempDir(), time.Second, false)}
	err := c.notifyNewObject(context.Background(), objects.HashBlobBytes([]byte("hello")))
	require.ErrorIs(t, err, ErrProviderNotify)
}
