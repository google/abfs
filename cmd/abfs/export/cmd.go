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

package export

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/ninjago/ninjalog"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

type exportCmd struct {
	*root.RootCmd
	*cmd.StoreProvider
	prune bool
	noCsv bool
}

type entry struct {
	e      *ninjalog.LogEntry
	req    *binding.EvalRequest
	resp   *binding.EvalResponse
	rdSize uint64
	wrSize uint64
}

// Global histogram tracking engine for parallel builds
type telemetryStats struct {
	mu           sync.Mutex
	readHisto    [8]uint64
	writeHisto   [8]uint64
	deleteHisto  [8]uint64
	maxReads     int
	maxWrites    int
	maxDeletes   int
	totalEntries uint64
}

var stats = &telemetryStats{}

func NewCmd(rootCmd *root.RootCmd) *exportCmd {
	return &exportCmd{RootCmd: rootCmd}
}

func (c *exportCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "export",
		Usage:   "[--prune] [--no-csv] <ninja-log>",
		Aliases: []string{},
		Desc:    `Export build data to csv file and display patch histograms.`,
		Hidden:  false,
	}
}

func (c *exportCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.prune, "prune", "", false, "Prune target outputs to remove duplication.")
	fl.BoolVarP(&c.noCsv, "no-csv", "", false, "Print raw data without commas and header.")
}

func parseLog(fileName string) (entries []ninjalog.NinjaLogEntries, retErr error) {
	f, err := os.Open(fileName)
	if err != nil {
		return nil, err
	}
	defer func() { retErr = entities.FirstError(retErr, f.Close()) }()
	return ninjalog.Parse(f)
}

func (c *exportCmd) fetchEvalResponse(ctx context.Context, hash entities.Hash) (*binding.EvalResponse, error) {
	buf, err := c.Store.GetObject(ctx, hash)
	if err != nil {
		return nil, err
	}
	result := new(binding.EvalResponse)
	if err := result.Decode(hash, buf); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *exportCmd) fetchEvalRequest(ctx context.Context, hash entities.Hash) (*binding.EvalRequest, error) {
	buf, err := c.Store.GetObject(ctx, hash)
	if err != nil {
		return nil, err
	}
	result := new(binding.EvalRequest)
	if err := result.Decode(hash, buf); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *exportCmd) fetchEntry(ctx context.Context, e *ninjalog.LogEntry) (*entry, error) {
	resp, err := c.fetchEvalResponse(ctx, e.RespHash)
	if err != nil {
		return nil, err
	}
	req, err := c.fetchEvalRequest(ctx, e.ReqHash)
	if err != nil {
		return nil, err
	}
	var rdSize uint64
	var wrSize uint64
	for _, p := range resp.ReadPatch {
		rdSize += uint64(p.Size)
	}
	for _, p := range resp.WritePatch {
		wrSize += uint64(p.Size)
	}
	return &entry{e: e, req: req, resp: resp, rdSize: rdSize, wrSize: wrSize}, nil
}

// Map transaction metric size lengths into power-of-two classifications
func getBucket(count int) int {
	switch {
	case count == 0:
		return 0 // Completely Clean No-op Step
	case count <= 4:
		return 1 // Micro modification patch
	case count <= 16:
		return 2
	case count <= 64:
		return 3 // Medium step batching
	case count <= 256:
		return 4
	case count <= 1024:
		return 5 // Massive phase wave
	case count <= 4096:
		return 6
	default:
		return 7 // Out-of-bounds heavy spill
	}
}

func (s *telemetryStats) Accumulate(e *entry) {
	rdLen := len(e.resp.ReadPatch)
	wrLen := len(e.resp.WritePatch)
	delLen := len(e.resp.Deletes)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.readHisto[getBucket(rdLen)]++
	s.writeHisto[getBucket(wrLen)]++
	s.deleteHisto[getBucket(delLen)]++

	if rdLen > s.maxReads {
		s.maxReads = rdLen
	}
	if wrLen > s.maxWrites {
		s.maxWrites = wrLen
	}
	if delLen > s.maxDeletes {
		s.maxDeletes = delLen
	}
	s.totalEntries++
}

func (s *telemetryStats) DisplayReport() {
	buckets := []string{"0", "1 - 4", "5 - 16", "17 - 64", "65 - 256", "257 - 1K", "1K - 4K", "> 4K"}

	fmt.Fprintln(os.Stderr, "\n=====================================================================")
	fmt.Fprintln(os.Stderr, "         ABFS PIPELINE ANALYSIS SUMMARY HISTOGRAM REPORT             ")
	fmt.Fprintln(os.Stderr, "=====================================================================")
	fmt.Fprintf(os.Stderr, "Total Evaluated Build Steps : %d\n", s.totalEntries)
	fmt.Fprintf(os.Stderr, "Peak Single-Step File Read Actions  : %d\n", s.maxReads)
	fmt.Fprintf(os.Stderr, "Peak Single-Step File Write Actions : %d\n", s.maxWrites)
	fmt.Fprintf(os.Stderr, "Peak Single-Step File Delete Actions: %d\n\n", s.maxDeletes)

	printSection := func(title string, counts [8]uint64) {
		fmt.Fprintf(os.Stderr, "--- %s Distribution counts ---\n", title)
		for i, label := range buckets {
			// Print raw count formatted alongside an intuitive ASCII metric trendline bar
			barLength := 0
			if s.totalEntries > 0 {
				barLength = int((counts[i] * 40) / s.totalEntries)
			}
			bar := strings.Repeat("#", barLength)
			fmt.Fprintf(os.Stderr, "  %-12s items : %-10d %s\n", label, counts[i], bar)
		}
		fmt.Fprintln(os.Stderr)
	}

	printSection("ReadPatch", s.readHisto)
	printSection("WritePatch", s.writeHisto)
	printSection("Deletes", s.deleteHisto)
	fmt.Fprintln(os.Stderr, "=====================================================================")
}

func printCsvHeader(w *csv.Writer) error {
	hdr := []string{
		"stage", "time", "duration", "cache-hit", "req-hash", "rsp-hash", "target", "pid",
		"cpu-pct", "rt-time", "rt-duration", "rt-snapshot", "rt-usertime", "rt-systime",
		"rt-memory", "rt-io-in", "rt-io-out", "rt-soft-fault", "rt-hard-fault", "rt-vol-ctx",
		"rt-forced-ctx", "rd-patch-cnt", "rd-missing-cnt", "wr-patch-cnt", "wr-missing-cnt",
		"del-patch-cnt", "total-rd", "total-wr",
	}
	return w.Write(hdr)
}

func printCsvRecord(w *csv.Writer, stage int, e *entry) error {
	rec := []string{
		strconv.Itoa(stage),
		strconv.FormatInt(e.e.Time.UnixNano(), 10),
		strconv.FormatInt(e.e.Duration.Milliseconds(), 10),
		string(e.e.RunType),
		e.e.ReqHash.String(),
		e.e.RespHash.String(),
		e.e.Target,
		strconv.Itoa(e.resp.RunInfo.Pid),
		fmt.Sprintf("%f", e.resp.CPUTime()),
		strconv.FormatInt(e.resp.RunInfo.Timestamp.UnixNano(), 10),
		fmt.Sprintf("%f", e.resp.RunInfo.RunDuration),
		fmt.Sprintf("%f", e.resp.RunInfo.SnapshotDuration),
		fmt.Sprintf("%f", e.resp.RunInfo.UserTime),
		fmt.Sprintf("%f", e.resp.RunInfo.SysTime),
		strconv.FormatInt(e.resp.RunInfo.Memory, 10),
		strconv.FormatInt(e.resp.RunInfo.IO.In, 10),
		strconv.FormatInt(e.resp.RunInfo.IO.Out, 10),
		strconv.FormatInt(e.resp.RunInfo.Misc.SoftFault, 10),
		strconv.FormatInt(e.resp.RunInfo.Misc.HardFault, 10),
		strconv.FormatInt(e.resp.RunInfo.Misc.VolContextSwitch, 10),
		strconv.FormatInt(e.resp.RunInfo.Misc.ForcedContextSwitch, 10),
		strconv.Itoa(len(e.resp.ReadPatch)),
		strconv.Itoa(len(e.resp.MissingReads)),
		strconv.Itoa(len(e.resp.WritePatch)),
		strconv.Itoa(len(e.resp.MissingWrites)),
		strconv.Itoa(len(e.resp.Deletes)),
		strconv.FormatUint(e.rdSize, 10),
		strconv.FormatUint(e.wrSize, 10),
	}
	return w.Write(rec)
}

func (c *exportCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	logFile := args.EnsureSingleArg(fl)

	logs, err := parseLog(logFile)
	if err != nil {
		return err
	}
	w := csv.NewWriter(os.Stdout)

	if c.noCsv {
		w.Comma = ' '
	} else {
		if err := printCsvHeader(w); err != nil {
			log.Fatal(err)
		}
	}

	for stage, l := range logs {
		ve := make([]*entry, len(l.Entries))
		g := new(errgroup.Group)
		g.SetLimit(runtime.NumCPU())

		for i, e := range l.Entries {
			if c.prune && i > 0 && l.Entries[i-1].RespHash == e.RespHash {
				ve[i] = nil
				continue
			}
			g.Go(func() error {
				le, err := c.fetchEntry(ctx, e)
				if err != nil {
					return err
				}
				ve[i] = le
				// Update statistics safely across asynchronous routine limits
				if le != nil {
					stats.Accumulate(le)
				}
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			log.Fatal(err)
		}

		for _, e := range ve {
			if e != nil {
				if err := printCsvRecord(w, stage, e); err != nil {
					log.Fatal(err)
				}
				w.Flush()
			}
		}
	}

	// Output report to stderr so it doesn't corrupt raw stdout redirect outputs
	stats.DisplayReport()

	return w.Error()
}
