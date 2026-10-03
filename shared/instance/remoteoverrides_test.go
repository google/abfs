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
	"gopkg.in/yaml.v3"
)

func TestConfigRemoteOverrides_UnmarshalYAML(t *testing.T) {
	yamlData := `
- select:
    classes:
      - a
- select:
    classes:
      - b
`
	var overrides ConfigRemoteOverrides
	err := yaml.Unmarshal([]byte(yamlData), &overrides)
	require.NoError(t, err, "Failed to unmarshal yaml")

	require.Len(t, overrides.Overrides, 2)
	require.NotEmpty(t, overrides.Overrides[0].Select.Classes)
	require.Equal(t, "a", overrides.Overrides[0].Select.Classes[0])

	require.NotEmpty(t, overrides.Overrides[1].Select.Classes)
	require.Equal(t, "b", overrides.Overrides[1].Select.Classes[0])
}
