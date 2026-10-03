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

package args

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

func TestChoice(t *testing.T) {
	type sampleOptions struct {
		opt string
	}

	newFS := func() (*pflag.FlagSet, *sampleOptions) {
		var o1 sampleOptions
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		OptChoice(fs, &o1.opt, "opt", "o", "default", []string{"a", "b", "c"}, "Usage")
		return fs, &o1
	}

	t.Run("empty", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{}))
		require.Equal(t, "default", o1.opt)
	})

	t.Run("simple", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--opt", "a"}))
		require.Equal(t, "a", o1.opt)
	})

	t.Run("simple equal", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--opt=a"}))
		require.Equal(t, "a", o1.opt)
	})

	t.Run("parse error", func(t *testing.T) {
		fs, _ := newFS()
		require.Error(t, fs.Parse([]string{"--opt=a", "--opt=xxx"}))
	})

	t.Run("unset with empty text", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--opt=a", "--opt="}))
		require.Equal(t, "default", o1.opt)
	})

}
