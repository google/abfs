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

package filebased

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pkg/xattr"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
)

const markXattr = "user.mark.%s"

type Marker struct {
	lock        sync.Mutex
	cond        *sync.Cond
	sema        chan struct{}
	server      *fileBasedServer
	pendings    map[Hash]*markObject
	bads        map[Hash]struct{}
	stack       []*markObject
	marked      atomic.Int64
	visited     atomic.Int64
	processing  atomic.Int64
	xattrActive atomic.Int64
	xattrWait   atomic.Int64
}

type markContext struct {
	context  context.Context
	lock     sync.Mutex
	key      string
	value    string
	count    atomic.Int64
	root     *markObject
	errs     []error
	callback func()
}

func (c *markContext) addError(err error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.errs = append(c.errs, err)
}

type markObject struct {
	ctx     *markContext
	degree  atomic.Int32
	hash    entities.Hash
	rev     []*markObject
	withErr bool
}

func (r *markObject) addError(err error) {
	r.withErr = true
	r.ctx.addError(err)
}

func NewMarker(server *fileBasedServer) *Marker {
	m := &Marker{
		server:   server,
		pendings: make(map[Hash]*markObject),
		bads:     make(map[Hash]struct{}),
		sema:     make(chan struct{}, 8000),
	}
	m.cond = sync.NewCond(&m.lock)
	return m
}

func (m *Marker) Mark(ctx context.Context, objHash entities.Hash, key, value string) (int64, error) {
	p := m.server.objectStore.Path(objHash)
	exists, err := m.fileHasAttr(p, key, value)
	if err != nil {
		return 0, err
	}
	if exists {
		return 0, nil
	}

	done := make(chan struct{})
	markCtx := &markContext{
		context: ctx,
		key:     key,
		value:   value,
		callback: func() {
			close(done)
		},
	}

	markCtx.root = &markObject{
		ctx:  markCtx,
		hash: objHash,
	}

	m.lock.Lock()
	m.stack = append(m.stack, markCtx.root)
	m.cond.Signal()
	m.lock.Unlock()

	<-done
	return markCtx.count.Load(), errors.Join(markCtx.errs...)
}

func (m *Marker) printStats(w io.Writer) {
	_, _ = fmt.Fprintf(w, "%s stack=%d pending=%d bads=%d processing=%d xattr-wait=%d xattr-active=%d marked=%d visited=%d \n", time.Now(),
		len(m.stack), len(m.pendings), len(m.bads), m.processing.Load(), m.xattrWait.Load(), m.xattrActive.Load(), m.marked.Load(), m.visited.Load())
}

func (m *Marker) getPending() *markObject {
	m.lock.Lock()
	defer m.lock.Unlock()

	for len(m.stack) == 0 {
		m.cond.Wait()
	}

	s := m.stack
	req := s[len(s)-1]
	m.stack = s[0 : len(s)-1]
	return req
}

func (m *Marker) Run() {
	go func() {
		for range time.NewTicker(10 * time.Second).C {
			m.printStats(os.Stdout)
		}
	}()
	for i := 0; i < runtime.NumCPU()*4; i++ {
		go func() {
			for {
				req := m.getPending()
				m.processing.Add(1)
				m.processPending(req)
				m.processing.Add(-1)
			}
		}()
	}
}

func (m *Marker) finalize(ctx context.Context, req *markObject) {
	if req == req.ctx.root {
		defer req.ctx.callback()
	}

	if !req.withErr {
		p := m.server.objectStore.Path(req.hash)
		if err := m.setFileAttr(p, req.ctx.key, req.ctx.value); err != nil {
			req.addError(err)
		} else {
			req.ctx.count.Add(1)
			m.marked.Add(1)
		}
	}

	m.lock.Lock()
	defer m.lock.Unlock()
	if req.withErr {
		m.bads[req.hash] = struct{}{}
	}
	for _, r := range req.rev {
		if req.withErr {
			r.withErr = true
		}
		if r.degree.Add(-1) == 0 {
			go m.finalize(ctx, r)
		}
	}
	delete(m.pendings, req.hash)
}

func (m *Marker) processPending(req *markObject) {
	ctx := req.ctx.context

	m.lock.Lock()
	_, isBad := m.bads[req.hash]
	m.lock.Unlock()
	if isBad {
		req.withErr = true
		m.finalize(ctx, req)
		return
	}

	m.visited.Add(1)
	links, err := m.links(ctx, req.hash)
	if err != nil {
		req.addError(err)
		m.finalize(ctx, req)
		return
	}
	var pendings []entities.Hash
	for _, link := range links {
		hdr, _, err := m.server.header(link)
		if err != nil {
			log.Printf("error finding server object %s for %s: %s", link, req.ctx.root.hash, err)
			req.addError(err)
			m.finalize(ctx, req)
			return
		}
		if entities.ObjectType(hdr.Type) == entities.ObjectTypeBlob {
			marked, err := m.markObject(link, req.ctx.key, req.ctx.value)
			if err != nil {
				log.Printf("error marking blob %s for %s: %s", link, req.ctx.root.hash, err)
				req.addError(err)
				m.finalize(ctx, req)
				return
			} else if marked {
				req.ctx.count.Add(1)
				m.marked.Add(1)
			}
			continue
		}
		p := m.server.objectStore.Path(link)
		exists, err := m.fileHasAttr(p, req.ctx.key, req.ctx.value)
		if err != nil {
			req.addError(err)
			m.finalize(ctx, req)
			return
		}
		if exists {
			continue
		}
		pendings = append(pendings, link)
	}
	if len(pendings) == 0 {
		m.finalize(ctx, req)
		return
	}

	m.lock.Lock()
	defer m.lock.Unlock()
	req.degree.Add(int32(len(pendings)))
	for _, link := range pendings {
		r := m.pendings[link]
		if r == nil {
			r = &markObject{
				ctx:  req.ctx,
				hash: link,
			}
			m.pendings[link] = r
			m.stack = append(m.stack, r)
		}
		r.rev = append(r.rev, req)
	}
	m.cond.Broadcast()
}

func (m *Marker) links(ctx context.Context, hash Hash) ([]Hash, error) {
	_, obj, err := remote.DecodeObject(ctx, m.server, hash)
	if err != nil {
		return nil, err
	}
	return obj.Links(), nil
}

func (m *Marker) markObject(hash Hash, key, value string) (bool, error) {
	p := m.server.objectStore.Path(hash)
	exists, err := m.fileHasAttr(p, key, value)
	if err != nil || exists {
		return false, err
	}
	if err := m.setFileAttr(p, key, value); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Marker) fileHasAttr(path, key, value string) (bool, error) {
	m.xattrWait.Add(1)
	m.sema <- struct{}{}
	defer func() { <-m.sema }()
	m.xattrWait.Add(-1)

	m.xattrActive.Add(1)
	defer m.xattrActive.Add(-1)

	buf, err := xattr.LGet(path, fmt.Sprintf(markXattr, key))
	if err != nil {
		var errno syscall.Errno
		if errors.As(err, &errno) && errno == syscall.ENODATA {
			return false, nil
		}
		return false, err
	}

	return string(buf) == value, nil
}

func (m *Marker) setFileAttr(path, key, value string) error {
	m.xattrWait.Add(1)
	m.sema <- struct{}{}
	defer func() { <-m.sema }()
	m.xattrWait.Add(-1)

	m.xattrActive.Add(1)
	defer m.xattrActive.Add(-1)

	return xattr.LSet(path, fmt.Sprintf(markXattr, key), []byte(value))
}
