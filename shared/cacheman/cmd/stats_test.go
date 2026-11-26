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

package cacheman

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStatsCmdVol(t *testing.T) {
	tests := []struct {
		name string
		size int64
		want string
	}{
		{name: "zero", size: 0, want: "0 B"},
		{name: "positive", size: 1000, want: "1.0 kB"},
		{name: "negative", size: -1000, want: "-1.0 kB"},
		{name: "negative megabytes", size: -160071680, want: "-160 MB"},
		{name: "min int64", size: -1 << 63, want: "-9.2 EB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &statsCmd{}
			c.resetNumbers()
			require.Equal(t, tt.want, c.vol(tt.size, 0))
		})
	}

	t.Run("deltas", func(t *testing.T) {
		c := &statsCmd{}
		c.resetNumbers()
		require.Equal(t, "2.0 kB", c.vol(2000, 0))

		c.resetNumbers()
		require.Equal(t, "1.0 kB (-1.0 kB)", c.vol(1000, 0))

		c.resetNumbers()
		require.Equal(t, "3.0 kB (+2.0 kB)", c.vol(3000, 0))
	})
}
