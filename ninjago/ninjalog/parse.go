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
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/abfs/shared/entities"
)

type RunType string

func (r RunType) IsLocal() bool {
	return r == RunTypeLocal || r == RunTypeFallbackBusy || r == RunTypeFallbackErr || r == RunTypeLocalNoTap
}
func (r RunType) IsCacheHit() bool {
	return r == RunTypeCached || r == RunTypeDelayedCached
}

const (
	RunTypeLocal         RunType = "local"
	RunTypeLocalNoTap    RunType = "local-no-tap"
	RunTypeFallbackBusy  RunType = "fallback-busy"
	RunTypeFallbackErr   RunType = "fallback-error"
	RunTypeRemote        RunType = "remote"
	RunTypeCached        RunType = "cached"
	RunTypeDelayedCached RunType = "delayed-cached"
)

// allRunTypes lists every RunType for formatting, so keep in sync with above
var allRunTypes = []RunType{
	RunTypeLocal,
	RunTypeLocalNoTap,
	RunTypeFallbackBusy,
	RunTypeFallbackErr,
	RunTypeRemote,
	RunTypeCached,
	RunTypeDelayedCached,
}

type LogEntry struct {
	Time       time.Time
	Duration   time.Duration
	RunType    RunType
	ReqHash    entities.Hash
	RespHash   entities.Hash
	Target     string
	Mismatches int
}

type NinjaLogEntries struct {
	Entries []*LogEntry
}

func (n *NinjaLog) Parse() (entries []NinjaLogEntries, retErr error) {
	f, err := os.Open(n.fileName)
	if err != nil {
		if entities.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer entities.CloseErrCheck(f, &retErr)

	return Parse(f)
}

func Parse(logReader io.Reader) ([]NinjaLogEntries, error) {
	var result []NinjaLogEntries
	s := bufio.NewScanner(logReader)

	var (
		ts       int64
		dur      float64
		runType  RunType
		reqHash  string
		respHash string
		invCount int
		target   string
	)

	var entries *[]*LogEntry
	newEntries := func() {
		result = append(result, NinjaLogEntries{})
		entries = &result[len(result)-1].Entries
	}
	for s.Scan() {
		if len(s.Text()) == 0 {
			continue
		}
		if s.Text()[0] == '#' {
			if strings.HasPrefix(s.Text(), ninjaLogHeader) {
				newEntries()
			}
			continue
		}
		if entries == nil {
			newEntries()
		}

		// Parse
		bb := bytes.NewBuffer(s.Bytes())
		n, err := fmt.Fscanf(bb, "%d %f %s %s %s %d ",
			&ts, &dur, &runType, &reqHash, &respHash, &invCount)
		if err != nil {
			return nil, err
		}
		if n != 6 {
			return nil, fmt.Errorf("expected %d arguments got %d", 6, n)
		}
		target, _ = bb.ReadString(0)
		target = strings.TrimSpace(target)

		var req, resp entities.Hash
		if _, err := hex.Decode(req[:], []byte(reqHash)); err != nil {
			return nil, err
		}
		if _, err := hex.Decode(resp[:], []byte(respHash)); err != nil {
			return nil, err
		}

		*entries = append(*entries, &LogEntry{
			Time:       time.Unix(ts/1000, (ts%1000)*1e6),
			Duration:   time.Duration(float64(time.Second) * dur),
			RunType:    runType,
			ReqHash:    req,
			RespHash:   resp,
			Target:     target,
			Mismatches: invCount,
		})
	}
	if s.Err() != nil {
		return nil, s.Err()
	}

	return result, nil
}
