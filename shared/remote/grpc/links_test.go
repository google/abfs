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

package grpc

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/remote"
)

func TestLinkValidationToStatus(t *testing.T) {
	// A large tree can report tens of thousands of missing children.
	distinct := make([]Hash, 27704)
	for i := range distinct {
		distinct[i] = objects.HashBlobBytes(fmt.Appendf(nil, "child %d", i))
	}
	// Every link appears twice, and the second copy of the first links comes before the later links.
	var repeated []Hash
	for i := 0; i < len(distinct); i += 100 {
		end := min(i+100, len(distinct))
		repeated = append(repeated, distinct[i:end]...)
		repeated = append(repeated, distinct[i:end]...)
	}

	tests := []struct {
		name      string
		links     []Hash
		wantLinks []Hash
		wantMsg   string
	}{
		{
			name:      "large",
			links:     repeated,
			wantLinks: distinct[:maxMissingLinksInStatus],
			wantMsg:   "One or more of the provided object's links is missing. Details: 27704 missing, 160 listed",
		},
		{
			name:      "small",
			links:     []Hash{distinct[1], distinct[0]},
			wantLinks: []Hash{distinct[1], distinct[0]},
			wantMsg:   "One or more of the provided object's links is missing. Details: 2 missing, 2 listed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			st, err := LinkValidationToStatus(&remote.ErrObjectLinkValidation{MissingLinks: tt.links})
			require.NoError(err)
			require.Equal(codes.FailedPrecondition, st.Code())
			require.Equal(tt.wantMsg, st.Message())

			var e *remote.ErrObjectLinkValidation
			require.ErrorAs(UnwrapMissingLinkError(st.Err()), &e)
			require.Equal(tt.wantLinks, e.MissingLinks)
		})
	}
}
