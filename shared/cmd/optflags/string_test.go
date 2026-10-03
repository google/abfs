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

func TestString(t *testing.T) {
	type sampleOptions struct {
		d1 *string
		d2 *string
	}

	newFS := func() (*pflag.FlagSet, *sampleOptions) {
		var o1 sampleOptions
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		OptString(fs, &o1.d1, "d1", "f", "Use d1")
		OptString(fs, &o1.d2, "d2", "g", "Use d2")
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
		require.Equal(t, "1m", *o1.d1)
		require.Nil(t, o1.d2)
	})

	t.Run("simple equal", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--d1=1m"}))
		require.Empty(t, fs.Args())
		require.NotNil(t, o1.d1)
		require.Equal(t, "1m", *o1.d1)
		require.Nil(t, o1.d2)
	})

	t.Run("simple shorthand", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--d1=1m", "-g", "5m"}))
		require.Empty(t, fs.Args())
		require.NotNil(t, o1.d1)
		require.Equal(t, "1m", *o1.d1)
		require.NotNil(t, o1.d2)
		require.Equal(t, "5m", *o1.d2)
	})

	t.Run("parse error", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--d1=1m", "--d2", "--d1=xxx"}))
		require.Empty(t, fs.Args())
		require.NotNil(t, o1.d1)
		require.Equal(t, "1m", *o1.d1)
		require.NotNil(t, o1.d2)
		require.Equal(t, "--d1=xxx", *o1.d2)
	})

	t.Run("unset with empty text", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--d1=1m", "-g=1m", "--d1="}))
		require.Empty(t, fs.Args())
		require.Nil(t, o1.d1)
		require.NotNil(t, o1.d2)
		require.Equal(t, "1m", *o1.d2)
	})

	t.Run("no values", func(t *testing.T) {
		fs, _ := newFS()
		require.Error(t, fs.Parse([]string{"--d1=1m", "--d2"}))
	})
}

func TestStringSlice(t *testing.T) {
	type sampleOptions struct {
		d1 *[]string
		d2 *[]string
	}

	newFS := func() (*pflag.FlagSet, *sampleOptions) {
		var o1 sampleOptions
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		OptStringSlice(fs, &o1.d1, "d1", "", "Use d1")
		OptStringSlice(fs, &o1.d2, "d2", "g", "Use d2")
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
		require.NoError(t, fs.Parse([]string{"--d1", "hello"}))
		require.Empty(t, fs.Args())
		require.NotNil(t, o1.d1)
		require.Equal(t, []string{"hello"}, *o1.d1)
		require.Nil(t, o1.d2)
	})

	t.Run("simple two options", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--d1=hello", "--d2", "world,1,2"}))
		require.Empty(t, fs.Args())
		require.NotNil(t, o1.d1)
		require.Equal(t, []string{"hello"}, *o1.d1)
		require.NotNil(t, o1.d2)
		require.Equal(t, []string{"world", "1", "2"}, *o1.d2)
	})

	t.Run("simple shorthand", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--d1=1", "-g", "5", "--d2", "14"}))
		require.Empty(t, fs.Args())
		require.NotNil(t, o1.d1)
		require.Equal(t, []string{"1"}, *o1.d1)
		require.NotNil(t, o1.d2)
		require.Equal(t, []string{"5", "14"}, *o1.d2)
	})

	t.Run("parse error next as value", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--d1=123", "--d2", "--d1=xxx"}))
		require.Empty(t, fs.Args())
		require.Equal(t, []string{"123"}, *o1.d1)
		require.NotNil(t, o1.d2)
		require.Equal(t, []string{"--d1=xxx"}, *o1.d2)
	})

	t.Run("unset with empty text", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--d1=123", "-g=10", "--d1="}))
		require.Empty(t, fs.Args())
		require.Nil(t, o1.d1)
		require.NotNil(t, o1.d2)
		require.Equal(t, []string{"10"}, *o1.d2)
	})

	t.Run("no values", func(t *testing.T) {
		fs, _ := newFS()
		require.Error(t, fs.Parse([]string{"--d1=hello", "--d2"}))
	})
}
