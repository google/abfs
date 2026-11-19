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

package server

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeBuildGoal(t *testing.T) {
	tests := []struct {
		name string
		goal string
		want string
	}{
		{name: "empty", goal: "", want: "unknown"},
		{name: "spaces", goal: "  ", want: "unknown"},
		{name: "unknown", goal: "unknown", want: "unknown"},
		{name: "droid", goal: "droid", want: "droid"},
		{name: "droid dist", goal: "droid dist", want: "dist"},
		{name: "dist droid", goal: "dist droid", want: "dist"},
		{name: "nothing", goal: "nothing", want: "nothing"},
		{name: "modules", goal: "libfoo libbar", want: "other"},
		{name: "soong bootstrap", goal: "out/soong/build.aosp_cf_x86_64_auto.ninja", want: "soong"},
		{name: "soong bootstrap absolute out dir", goal: "/home/u/o/soong/build.p.ninja", want: "soong"},
		{name: "soong bootstrap without product", goal: "out/soong/build.ninja", want: "soong"},
		{name: "build ninja outside soong", goal: "out/build.ninja", want: "other"},
		{name: "other soong ninja file", goal: "out/soong/other.ninja", want: "other"},
		{name: "modules in", goal: "MODULES-IN-frameworks-base", want: "other"},
		{name: "many targets", goal: strings.Repeat("libfoo ", 10000), want: "other"},
		{name: "older client command with goal", goal: "build/soong/soong_ui.bash --make-mode -j32 droid", want: "droid"},
		{name: "older client command without goal", goal: "/home/u/build.sh -j32", want: "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, normalizeBuildGoal(tt.goal))
		})
	}
}
