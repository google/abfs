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

package optflags

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

func TestBool(t *testing.T) {
	type sampleOptions struct {
		feature1 *bool
		feature2 *bool
	}

	newFS := func() (*pflag.FlagSet, *sampleOptions) {
		var o1 sampleOptions
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		OptBool(fs, &o1.feature1, "feature1", "f", "Use feature1")
		OptBool(fs, &o1.feature2, "feature2", "g", "Use feature2")
		return fs, &o1
	}

	t.Run("empty", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{}))
		require.Nil(t, o1.feature1)
		require.Nil(t, o1.feature2)
	})

	t.Run("simple", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--feature1"}))
		require.Empty(t, fs.Args())
		require.NotNil(t, o1.feature1)
		require.True(t, *o1.feature1)
		require.Nil(t, o1.feature2)
	})

	t.Run("simple equal", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--feature1=true"}))
		require.Empty(t, fs.Args())
		require.NotNil(t, o1.feature1)
		require.True(t, *o1.feature1)
		require.Nil(t, o1.feature2)
	})

	t.Run("simple shorthand", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"-f"}))
		require.Empty(t, fs.Args())
		require.NotNil(t, o1.feature1)
		require.True(t, *o1.feature1)
		require.Nil(t, o1.feature2)
	})

	t.Run("parse error", func(t *testing.T) {
		fs, _ := newFS()
		require.Error(t, fs.Parse([]string{"--feature1=true", "--feature2", "--feature1=xxx"}))
	})

	t.Run("unset with empty text", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--feature1=true", "--feature2", "--feature1="}))
		require.Empty(t, fs.Args())
		require.Nil(t, o1.feature1)
		require.NotNil(t, o1.feature2)
		require.True(t, *o1.feature2)
	})
}
