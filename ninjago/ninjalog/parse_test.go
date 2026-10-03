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

package ninjalog

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseNinjaLog(t *testing.T) {
	ninjaLog := `
# ninjago log v1
# ninjago log v1
#Timestamp Duration Cached Request-Hash                             Response-Hash                            Target
1706078570    0.113 false  9fc16ced427abbc72fd691d357a464f05b27f33a 77b4f67f4f19d2c8505c839752cabb3db2735fe5   0 out/soong/globs/aosp_cf_x86_64_phone/693
1706078570    0.252 false  b351327806a4e9d84efafa7fb9465b3781bae7a6 41909b2a56208dbb4e8e0cb5e607ed766078b8bb   0 out/soong/globs/aosp_cf_x86_64_phone/546
# ninjago log v1
#Timestamp Duration Cached Request-Hash                             Response-Hash                            Target
1706079151    0.032 true  6d0ff12955a80b459fc12ee4398b22bf33e32b46 472dcab4561cdb3f4c51df9d1ef101aad2e21462   0 out/soong/.intermediates/hardware/interfaces/radio/aidl/android.hardware
# ninjago log v1
`
	logs, err := Parse(strings.NewReader(ninjaLog))

	require.NoError(t, err)
	require.Len(t, logs, 4)
	require.Len(t, logs[0].Entries, 0)
	require.Len(t, logs[1].Entries, 2)
	require.Len(t, logs[2].Entries, 1)
	require.Len(t, logs[0].Entries, 0)
}

func TestParseWrapLog(t *testing.T) {
	ninjaLog := `
1706078570    0.113 false  9fc16ced427abbc72fd691d357a464f05b27f33a 77b4f67f4f19d2c8505c839752cabb3db2735fe5   0 sample command with space
1706078570    0.252 false  b351327806a4e9d84efafa7fb9465b3781bae7a6 41909b2a56208dbb4e8e0cb5e607ed766078b8bb   0 out/soong/globs/aosp_cf_x86_64_phone/546
`
	logs, err := Parse(strings.NewReader(ninjaLog))

	require.NoError(t, err)
	require.Len(t, logs, 1)
	require.Len(t, logs[0].Entries, 2)
	require.Equal(t, "sample command with space", logs[0].Entries[0].Target)
	require.Equal(t, "out/soong/globs/aosp_cf_x86_64_phone/546", logs[0].Entries[1].Target)
}
