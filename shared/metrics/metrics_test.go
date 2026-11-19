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

package metrics

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInit(t *testing.T) {
	require := require.New(t)
	prev := GetProvider()
	t.Cleanup(func() { SetProvider(prev) })

	Init() // the default provider is not lazy
	calls := 0
	SetProvider(NewLazyProvider(func() Provider {
		calls++
		return NewNoopProvider()
	}))
	require.Equal(0, calls, "a lazy provider is not built when set")
	Init()
	require.Equal(1, calls)
	Init()
	GetProvider().NewCounter("abfs_test_total", "Count of tests", nil).Inc()
	require.Equal(1, calls, "the provider is built once")
}
