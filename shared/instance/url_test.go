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

package instance

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaybeRemoveScheme(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		in   string
		want string
	}{
		{
			in:   "android.googlesource.com",
			want: "android.googlesource.com",
		},
		{
			in:   "https://android.googlesource.com",
			want: "android.googlesource.com",
		},
		{
			in:   "https://android.googlesource.com/child1/child2",
			want: "android.googlesource.com/child1/child2",
		},
		{
			in:   "android.googlesource.com:2914",
			want: "android.googlesource.com:2914",
		},
		{
			in:   "https://android.googlesource.com:2914",
			want: "android.googlesource.com:2914",
		},
	}

	for _, test := range tests {
		require.Equal(test.want, MaybeRemoveScheme(test.in))
	}
}

func TestMaybeAddScheme(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		in   string
		want string
	}{
		{
			in:   "android.googlesource.com",
			want: "https://android.googlesource.com",
		},
		{
			in:   "android.googlesource.com/child1/child2",
			want: "https://android.googlesource.com/child1/child2",
		},
		{
			in:   "https://android.googlesource.com",
			want: "https://android.googlesource.com",
		},
		{
			in:   "android.googlesource.com:2914",
			want: "https://android.googlesource.com:2914",
		},
		{
			in:   "ssh://android.googlesource.com:2914",
			want: "ssh://android.googlesource.com:2914",
		},
	}
	for _, test := range tests {
		require.Equal(test.want, MaybeAddScheme(test.in, "https"))
	}
}
