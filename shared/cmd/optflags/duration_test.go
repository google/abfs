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
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

func TestDuration(t *testing.T) {
	type sampleOptions struct {
		d1 *time.Duration
		d2 *time.Duration
	}

	newFS := func() (*pflag.FlagSet, *sampleOptions) {
		var o1 sampleOptions
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		OptDuration(fs, &o1.d1, "d1", "", "Use d1")
		OptDuration(fs, &o1.d2, "d2", "g", "Use d2")
		return fs, &o1
	}

	t.Run("empty", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{}))
		require.Nil(t, o1.d1)
		require.Nil(t, o1.d2)
	})

	t.Run("simple", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--d1", "1m"}))
		require.Empty(t, fs.Args())
		require.NotNil(t, o1.d1)
		require.Equal(t, time.Minute, *o1.d1)
		require.Nil(t, o1.d2)
	})

	t.Run("simple equal", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--d1=1m"}))
		require.Empty(t, fs.Args())
		require.NotNil(t, o1.d1)
		require.Equal(t, time.Minute, *o1.d1)
		require.Nil(t, o1.d2)
	})

	t.Run("simple shorthand", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--d1=1m", "-g", "5m"}))
		require.Empty(t, fs.Args())
		require.NotNil(t, o1.d1)
		require.Equal(t, time.Minute, *o1.d1)
		require.NotNil(t, o1.d2)
		require.Equal(t, 5*time.Minute, *o1.d2)
	})

	t.Run("parse error", func(t *testing.T) {
		fs, _ := newFS()
		require.Error(t, fs.Parse([]string{"--d1=1m", "--d1=xxx"}))
	})

	t.Run("unset with empty text", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--d1=1m", "-g=1m", "--d1="}))
		require.Empty(t, fs.Args())
		require.Nil(t, o1.d1)
		require.NotNil(t, o1.d2)
		require.Equal(t, time.Minute, *o1.d2)
	})

	t.Run("no values", func(t *testing.T) {
		fs, _ := newFS()
		require.Error(t, fs.Parse([]string{"--d1=1m", "--d2"}))
	})
}
