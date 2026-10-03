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

package abfsrepo

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupsMatch(t *testing.T) {
	tests := []struct {
		name          string
		prjGroups     []string
		filterInclude map[string]struct{}
		filterExclude map[string]struct{}
		want          bool
	}{
		{
			name:          "basic include",
			prjGroups:     []string{"g1"},
			filterInclude: map[string]struct{}{"g1": {}},
			filterExclude: map[string]struct{}{},
			want:          true,
		},
		{
			name:          "basic exclude",
			prjGroups:     []string{"g1"},
			filterInclude: map[string]struct{}{"g1": {}},
			filterExclude: map[string]struct{}{"g1": {}},
			want:          false,
		},
		{
			name:          "no match include",
			prjGroups:     []string{"g1"},
			filterInclude: map[string]struct{}{"g2": {}},
			filterExclude: map[string]struct{}{},
			want:          false,
		},
		{
			name:          "exclude all",
			prjGroups:     []string{"g1"},
			filterInclude: map[string]struct{}{"g1": {}},
			filterExclude: map[string]struct{}{"all": {}},
			want:          false,
		},
		{
			name:          "include all",
			prjGroups:     []string{"g1"},
			filterInclude: map[string]struct{}{"all": {}},
			filterExclude: map[string]struct{}{},
			want:          true,
		},
		{
			name:          "empty project groups defaults to default",
			prjGroups:     nil,
			filterInclude: map[string]struct{}{"default": {}},
			filterExclude: map[string]struct{}{},
			want:          true,
		},
		{
			name:          "default filter matches project without notdefault",
			prjGroups:     []string{"g1"},
			filterInclude: map[string]struct{}{"default": {}},
			filterExclude: map[string]struct{}{},
			want:          true,
		},
		{
			name:          "default filter does not match project with notdefault",
			prjGroups:     []string{"g1", "notdefault"},
			filterInclude: map[string]struct{}{"default": {}},
			filterExclude: map[string]struct{}{},
			want:          false,
		},
		{
			name:          "all filter should match notdefault project group",
			prjGroups:     []string{"g1", "notdefault"},
			filterInclude: map[string]struct{}{"all": {}},
			filterExclude: map[string]struct{}{},
			want:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GroupsMatch(tt.prjGroups, tt.filterInclude, tt.filterExclude)
			require.Equal(t, tt.want, got, "GroupsMatch()")
		})
	}
}
