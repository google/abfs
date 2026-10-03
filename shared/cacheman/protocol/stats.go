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

package protocol

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"
)

type OverallPushStat struct {
	BrokenLinks    int64
	MissingObjects int64
}

type PushGroupStat struct {
	Requests int64
	Queued   int64
	Blocked  int64
	Pushing  int64
	Failed   int64
}

type ExecStat struct {
	Request int64
	Success int64
	Failed  int64
}

type QueueStat struct {
	Input   atomic.Int64
	Output  atomic.Int64
	Active  atomic.Int64
	Retries atomic.Int64
	Errors  atomic.Int64
}

var _ json.Marshaler = (*QueueStat)(nil)

func (qs *QueueStat) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Input   int64
		Output  int64
		Active  int64
		Retries int64
		Errors  int64
	}{
		Input:   qs.Input.Load(),
		Output:  qs.Output.Load(),
		Active:  qs.Active.Load(),
		Retries: qs.Retries.Load(),
		Errors:  qs.Errors.Load(),
	})
}

func (qs *QueueStat) UnmarshalJSON(buf []byte) error {
	var s struct {
		Input   int64
		Output  int64
		Active  int64
		Retries int64
		Errors  int64
	}
	if err := json.Unmarshal(buf, &s); err != nil {
		return err
	}
	qs.Input.Store(s.Input)
	qs.Output.Store(s.Output)
	qs.Active.Store(s.Active)
	qs.Retries.Store(s.Retries)
	qs.Errors.Store(s.Errors)
	return nil
}

type PushStat struct {
	Request atomic.Int64
	Report  atomic.Int64
}

type ExecutionStat struct {
	Request atomic.Int64
	Success atomic.Int64
	Failed  atomic.Int64
}

func (p *PushStat) Diff() string {
	v := p.Request.Load()
	r := p.Report.Load()
	if v == r {
		return fmt.Sprintf("%d/%d", v, 0)
	}
	s := fmt.Sprintf("%d/+%d", v, v-r)
	p.Report.Store(v)
	return s
}

type ClientStat struct {
	BytesSent     int64
	LastSent      time.Time
	BytesReceived int64
	LastReceived  time.Time
}

type CacheManStats struct {
	Connected    bool
	TotalSize    int64
	LRUSize      int64
	LocalCount   int64
	IndexCount   int64
	GCStart      time.Time
	GCEnd        time.Time
	EvictStart   time.Time
	EvictEnd     time.Time
	EvictCount   int64
	OldestObject time.Time
	ClientStat
	FetchBlobs    *QueueStat
	FetchTrees    *QueueStat
	FetchObjects  *QueueStat
	FetchARIs     *QueueStat
	FetchCASBlobs *QueueStat
	FetchMaps     *QueueStat
	OverallPushStat
	PushBlobs   PushGroupStat
	PushObjects PushGroupStat
	PushTrees   PushGroupStat
	PushRefs    PushGroupStat
	PushMaps    PushGroupStat
	Phony       PushGroupStat
	Execute     ExecStat
	Fallback    FallbackStats
}

type FallbackStats struct {
	Methods map[string]int64 `json:"methods"`
	Types   map[string]int64 `json:"types"`
}
