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
	"crypto/sha1"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

// columnStarts returns the offsets at which each whitespace-separated column
// of line starts, considering at most n columns.
func columnStarts(line string, n int) []int {
	var starts []int
	for _, loc := range regexp.MustCompile(`\S+`).FindAllStringIndex(line, n) {
		starts = append(starts, loc[0])
	}
	return starts
}

// columnEnds returns the offsets at which each whitespace-separated column of
// line ends, considering at most n columns.
func columnEnds(line string, n int) []int {
	var ends []int
	for _, loc := range regexp.MustCompile(`\S+`).FindAllStringIndex(line, n) {
		ends = append(ends, loc[1])
	}
	return ends
}

func TestWriteEntryColumnsAligned(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), DefaultLogFileName)
	nl := New(logFile)
	require.NoError(t, nl.OpenForWrite())

	ts := time.UnixMilli(1760000000123)
	var want []*LogEntry
	for i, rt := range allRunTypes {
		e := &LogEntry{
			Time:       ts,
			Duration:   time.Duration(i*1234) * time.Millisecond,
			RunType:    rt,
			ReqHash:    entities.Hash(sha1.Sum([]byte("req" + string(rt)))),
			RespHash:   entities.Hash(sha1.Sum([]byte("resp" + string(rt)))),
			Target:     "out/target with spaces/" + string(rt),
			Mismatches: i * 3,
		}
		want = append(want, e)
		require.NoError(t, nl.WriteEntry(e))
	}
	require.NoError(t, nl.Close())

	data, err := os.ReadFile(logFile)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	require.Len(t, lines, 2+len(allRunTypes))
	require.Equal(t, ninjaLogHeader, lines[0])

	header := lines[1]
	require.True(t, strings.HasPrefix(header, "#"))
	require.Len(t, columnStarts(header, -1), 7, "header %q", header)

	// Left-aligned columns start where their heading starts; right-aligned
	// columns (Duration, Mismatch) end where their heading ends.
	headerStarts := columnStarts(header, 7)
	headerEnds := columnEnds(header, 7)
	for _, line := range lines[2:] {
		starts := columnStarts(line, 7)
		ends := columnEnds(line, 7)
		require.Len(t, starts, 7, "entry %q", line)
		for _, col := range []int{0, 2, 3, 4, 6} {
			require.Equal(t, headerStarts[col], starts[col], "column %d start in\n%s\n%s", col, header, line)
		}
		for _, col := range []int{1, 5} {
			require.Equal(t, headerEnds[col], ends[col], "column %d end in\n%s\n%s", col, header, line)
		}
	}

	// The aligned output still parses.
	logs, err := nl.Parse()
	require.NoError(t, err)
	require.Len(t, logs, 1)
	require.Len(t, logs[0].Entries, len(want))
	for i, got := range logs[0].Entries {
		require.Equal(t, want[i].Time.UnixMilli(), got.Time.UnixMilli())
		require.InDelta(t, want[i].Duration.Seconds(), got.Duration.Seconds(), 0.001)
		require.Equal(t, want[i].RunType, got.RunType)
		require.Equal(t, want[i].ReqHash, got.ReqHash)
		require.Equal(t, want[i].RespHash, got.RespHash)
		require.Equal(t, want[i].Mismatches, got.Mismatches)
		require.Equal(t, want[i].Target, got.Target)
	}
}
