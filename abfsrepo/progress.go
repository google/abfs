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
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"
)

type syncProgressItem struct {
	task  string
	start time.Time
}

type SyncProgress struct {
	prefix     string
	lock       sync.Mutex
	wg         sync.WaitGroup
	cancel     context.CancelFunc
	total      int
	start      time.Time
	done       int
	actives    *list.List
	activesMap map[string]*list.Element
	notify     chan struct{}
}

func NewProgress(prefix string, total int) *SyncProgress {
	ctx, cancel := context.WithCancel(context.Background())
	p := &SyncProgress{
		prefix:     prefix,
		cancel:     cancel,
		start:      time.Now(),
		total:      total,
		actives:    list.New(),
		activesMap: make(map[string]*list.Element),
		notify:     make(chan struct{}),
	}
	p.wg.Add(1)
	go p.Watch(ctx)
	return p
}

func (p *SyncProgress) Watch(ctx context.Context) {
	defer p.wg.Done()
	timer := time.NewTicker(1 * time.Second)
	defer timer.Stop()

	fmt.Printf("\033[s")

	for {
		select {
		case <-timer.C:
		case <-p.notify:
		case <-ctx.Done():
			fmt.Printf("\033[u\033[K")
			return
		}
		p.printStatus()
	}
}

func (p *SyncProgress) printStatus() {
	p.lock.Lock()
	defer p.lock.Unlock()
	// Use integer division for percent so it's the floor() and we don't show 100% when still processing.
	var percent int
	if p.total > 0 {
		percent = p.done * 100 / p.total
	}
	fmt.Printf("\033[u\033[K")
	t := time.Since(p.start)
	var oldest string
	if top := p.actives.Front(); top != nil {
		item := top.Value.(syncProgressItem)
		ot := time.Since(item.start)
		oldest = fmt.Sprintf("%d:%02d %s", int(ot.Minutes()), int(ot.Seconds())%60, item.task)
	}
	fmt.Printf("%s %3d%% (%d/%d) %d:%02d | %d jobs | %s",
		p.prefix, percent, p.done, p.total, int(t.Minutes()), int(t.Seconds())%60, p.actives.Len(), oldest)
}

func (p *SyncProgress) notifyPrint() {
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

func (p *SyncProgress) StartTask(task string) {
	p.lock.Lock()
	defer p.lock.Unlock()

	e := p.actives.PushBack(syncProgressItem{
		task:  task,
		start: time.Now(),
	})
	p.activesMap[task] = e
	p.notifyPrint()
}

func (p *SyncProgress) EndTask(task string, success bool) {
	p.lock.Lock()
	defer p.lock.Unlock()

	if success {
		p.done++
	}

	node := p.activesMap[task]
	if node != nil {
		p.actives.Remove(node)
	}
	p.notifyPrint()
}

func (p *SyncProgress) Stop() {
	p.cancel()
	p.wg.Wait()

	p.lock.Lock()
	defer p.lock.Unlock()
	// Use integer division for percent so it's the floor() and we don't show 100% when still processing.
	var percent int
	if p.total > 0 {
		percent = p.done * 100 / p.total
	}
	fmt.Printf("%s %3d%% (%d/%d), done in %s\n\n", p.prefix, percent, p.done, p.total, time.Since(p.start))
}
