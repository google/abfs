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

	"github.com/google/abfs/shared/instance"
)

func TestVar(t *testing.T) {
	type sampleOptions struct {
		d1 *instance.CASFSType
		d2 *instance.AuthType
	}

	newFS := func() (*pflag.FlagSet, *sampleOptions) {
		var o1 sampleOptions
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		OptVar(fs, &o1.d1, "d1", "", "Use d1")
		OptVar(fs, &o1.d2, "d2", "g", "Use d2")
		return fs, &o1
	}

	t.Run("empty", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{}))
		require.Nil(t, o1.d1)
		require.Nil(t, o1.d2)
	})

	t.Run("simple equal", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--d1=kmod"}))
		require.Empty(t, fs.Args())
		require.NotNil(t, o1.d1)
		require.Equal(t, instance.CASFSTypeKmod, *o1.d1)
		require.Nil(t, o1.d2)
	})

	t.Run("unset with empty text", func(t *testing.T) {
		fs, o1 := newFS()
		require.NoError(t, fs.Parse([]string{"--d1=kmod", "-g=none", "--d1="}))
		require.Empty(t, fs.Args())
		require.Nil(t, o1.d1)
		require.NotNil(t, o1.d2)
		require.Equal(t, instance.AuthTypeNone, *o1.d2)
	})

	t.Run("no values", func(t *testing.T) {
		fs, _ := newFS()
		require.Error(t, fs.Parse([]string{"--d1=kmod", "--d2"}))
	})
}
