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

package gitindex

import (
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// PrintDump writes a detailed, formatted dump of the index to w.
func (d *DumpResult) PrintDump(w io.Writer) {
	fmt.Fprintf(w, "=== Git Index (Version %d, Entries: %d, Trees: %d) ===\n", d.Version, len(d.Entries), len(d.Trees))
	fmt.Fprintf(w, "Checksum: %s\n\n", hex.EncodeToString(d.Checksum[:]))

	if len(d.UnsupportedExtensions) > 0 {
		fmt.Fprintf(w, "!! UNSUPPORTED MANDATORY EXTENSIONS: %s\n",
			strings.Join(d.UnsupportedExtensions, ", "))
		fmt.Fprintf(w, "!! The entries below are INCOMPLETE and must not be trusted.\n")
	}

	fmt.Fprintln(w, "--- Entries ---")
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "Mode\tSize\tMtime\tSHA-1\tDev:Ino\tPath")
	for _, e := range d.Entries {
		mtime := time.Unix(int64(e.MtimeSec), int64(e.MtimeNsec)).Format("2006-01-02 15:04:05.000")
		fmt.Fprintf(tw, "%06o\t%d\t%s\t%s\t%d:%d\t%s\n",
			e.Mode, e.Size, mtime, hex.EncodeToString(e.Sha[:]), e.Dev, e.Ino, e.Path)
	}
	tw.Flush()

	if len(d.Trees) > 0 {
		fmt.Fprintln(w, "\n--- Cached Trees (TREE Extension) ---")
		twTree := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
		fmt.Fprintln(twTree, "Entries\tSubtrees\tSHA-1\tPath")
		for _, t := range d.Trees {
			shaStr := "INVALID (recompute on commit)"
			if t.EntryCount >= 0 {
				shaStr = hex.EncodeToString(t.Sha[:])
			}
			fmt.Fprintf(twTree, "%d\t%d\t%s\t%q\n", t.EntryCount, t.SubtreeCount, shaStr, t.Name)
		}
		twTree.Flush()
	}
}
