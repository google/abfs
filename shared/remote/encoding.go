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

package remote

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/zstd"
)

const (
	EncodingRaw   = "raw"
	EncodingZStd  = "zstd"
	EncodingLinks = "links"
)

func NewZStdContentReader(content []byte) ContentReadCloser {
	compressed, _ := zstd.Compress(content)
	return &ContentReader{
		reader:   bytes.NewReader(compressed),
		close:    nil,
		encoding: EncodingZStd,
	}
}

func DecodeToRaw(reader ContentReadCloser) (ContentReadCloser, error) {
	switch reader.Encoding() {
	case "", EncodingRaw:
		return reader, nil
	case EncodingZStd:
		r := zstd.NewReader(reader)
		return NewContentReader(r, EncodingRaw), nil
	case EncodingLinks:
		r := NewLinkReader(reader)
		return NewContentReader(r, EncodingRaw), nil
	default:
		return nil, fmt.Errorf("unknown encoding %q", reader.Encoding())
	}
}

func zstdCompressReader(input ContentReadCloser) ContentReadCloser {
	pr, pw := io.Pipe()
	zstdWriter := zstd.NewWriter(pw)

	go func() {
		var copyErr error
		var zstdCloseErr error

		_, copyErr = io.Copy(zstdWriter, input)

		zstdCloseErr = zstdWriter.Close()

		if copyErr != nil {
			_ = pw.CloseWithError(copyErr)
		} else if zstdCloseErr != nil {
			_ = pw.CloseWithError(zstdCloseErr)
		} else {
			_ = pw.Close()
		}
	}()

	return &ContentReader{
		reader: pr,
		close: func() error {
			err := input.Close()
			entities.CloseErrCheck(pw, &err)
			return err
		},
		encoding: EncodingZStd,
	}
}

func EncodeToZStd(reader ContentReadCloser) (ContentReadCloser, error) {
	switch reader.Encoding() {
	case EncodingZStd:
		return reader, nil
	case "", EncodingRaw:
		return zstdCompressReader(reader), nil
	default:
		return nil, fmt.Errorf("unknown encoding %q", reader.Encoding())
	}
}

func DecodeObject(ctx context.Context, server RemoteProvider, hash Hash) (t objects.ObjectType, obj objects.Object, retErr error) {
	reader, err := server.GetObject(ctx, hash)
	if err != nil {
		return "", nil, err
	}
	defer entities.CloseErrCheck(reader, &retErr)

	reader, err = DecodeToRaw(reader)
	if err != nil {
		return "", nil, err
	}
	defer entities.CloseErrCheck(reader, &retErr)

	content, err := io.ReadAll(reader)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read the provided object %q, err: %v", hash, err)
	}

	t, obj, err = objects.Factory.Decode(hash, content)
	if err != nil {
		return "", nil, fmt.Errorf("failed to decode the provided object %q, err: %v", hash, err)
	}

	return t, obj, err
}

const (
	LinkTypeBase64 = "base64"
	LinkTypeURL    = "url"
)

type ContentLink struct {
	LinkType string `json:"link_type"`
	Size     uint64 `json:"size"`
	Content  string `json:"content,omitempty"`
}

type LinksContent struct {
	Encoding string
	Links    []ContentLink
}

func loadLinksContent(reader io.ReadCloser) (links LinksContent, retErr error) {
	defer entities.CloseErrCheck(reader, &retErr)

	buf, err := io.ReadAll(reader)
	if err != nil {
		return LinksContent{}, err
	}

	return links, json.Unmarshal(buf, &links)
}

func NewLinkReader(reader io.ReadCloser) ContentReadCloser {
	linkContent, err := loadLinksContent(reader)
	if err == nil {
		lr := newLinkReader(linkContent.Links)
		var r ContentReadCloser
		r, err = DecodeToRaw(NewContentReader(lr, linkContent.Encoding))
		if err == nil {
			return r
		}
	}
	return &ContentReader{
		reader:   errReader{err: err},
		encoding: "raw",
	}
}

func NewLinkReaderWithContext(ctx context.Context, reader io.ReadCloser, httpClient *http.Client) ContentReadCloser {
	linkContent, err := loadLinksContent(reader)
	if err == nil {
		lr := newLinkReaderWithContext(ctx, httpClient, linkContent.Links)
		var r ContentReadCloser
		r, err = DecodeToRaw(NewContentReader(lr, linkContent.Encoding))
		if err == nil {
			return r
		}
	}
	return &ContentReader{
		reader:   errReader{err: err},
		encoding: "raw",
	}
}

const (
	DefaultMaxConcurrentSegments = 16 // up to 16 concurrent range downloads
	DefaultMaxRetries            = 5  // up to 5 retries per segment on network/server errors
)

var SegmentSize int64 = 16 * 1024 * 1024 // 16 MiB per range segment by default

func SetSegmentSize(size int64) {
	if size > 0 {
		SegmentSize = size
	}
}

var defaultPooledHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		MaxConnsPerHost:       128,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

type segmentTask struct {
	index int
	link  ContentLink
	start int64
	end   int64 // inclusive, or -1 if unknown size/base64
}

type segmentResult struct {
	index int
	data  []byte
	err   error
}

type concurrentLinkReader struct {
	ctx        context.Context    // Context managing lifetime of segment tasks.
	cancel     context.CancelFunc // Cancel function to abort in-flight workers on error or close.
	httpClient *http.Client
	tasks      []segmentTask

	taskChan   chan segmentTask // Channel for dispatching tasks to worker goroutines.
	resultChan chan segmentResult
	closeChan  chan struct{}
	closeOnce  sync.Once
	wg         sync.WaitGroup

	nextIndex int                   // Sequence index of the next segment expected by Read.
	pending   map[int]segmentResult // Buffer for out-of-order segment results keyed by task index.
	currBuf   *bytes.Reader         // Reader for the currently active segment's byte content.
	firstErr  error
}

var _ io.ReadCloser = (*concurrentLinkReader)(nil)

// Close stops background workers, cancels context, and releases reading resources.
// Note that Close() is not safe to call concurrently.
func (r *concurrentLinkReader) Close() error {
	r.closeOnce.Do(func() {
		close(r.closeChan)
		r.cancel()
	})
	r.nextIndex = len(r.tasks)
	r.currBuf = nil
	return nil
}

// dispatcher pushes segment tasks to workers until complete, cancelled, or closed.
func (r *concurrentLinkReader) dispatcher() {
	defer close(r.taskChan)
	for _, task := range r.tasks {
		select {
		case <-r.ctx.Done():
			return
		case <-r.closeChan:
			return
		case r.taskChan <- task:
		}
	}
}

// worker processes segment tasks from taskChan and posts results to resultChan.
func (r *concurrentLinkReader) worker() {
	defer r.wg.Done()
	for task := range r.taskChan {
		data, err := fetchTask(r.ctx, r.httpClient, task)
		select {
		case <-r.ctx.Done():
			return
		case <-r.closeChan:
			return
		case r.resultChan <- segmentResult{index: task.index, data: data, err: err}:
		}
	}
}

// fetchTask executes a single segment task via base64 decoding or HTTP range request with retries.
func fetchTask(ctx context.Context, httpClient *http.Client, task segmentTask) ([]byte, error) {
	switch task.link.LinkType {
	case "", LinkTypeBase64:
		return io.ReadAll(base64.NewDecoder(base64.StdEncoding, strings.NewReader(task.link.Content)))
	case LinkTypeURL:
		urlStr := task.link.Content
		if urlStr == "" {
			return nil, fmt.Errorf("empty url for link at position %d", task.index)
		}
		return RobustFetchRange(ctx, httpClient, urlStr, task.start, task.end, DefaultMaxRetries, 100*time.Millisecond)
	default:
		return nil, fmt.Errorf("unknown link type %q at task index %d", task.link.LinkType, task.index)
	}
}

// popPendingSegment extracts and removes the next expected segment from pending if available.
func (r *concurrentLinkReader) popPendingSegment() (segmentResult, bool) {
	res, ok := r.pending[r.nextIndex]
	if ok {
		delete(r.pending, r.nextIndex)
	}
	return res, ok
}

// applySegment sets currBuf for a successful result or cancels workers and returns an error.
func (r *concurrentLinkReader) applySegment(res segmentResult) error {
	if res.err != nil {
		r.cancel()
		return res.err
	}
	r.currBuf = bytes.NewReader(res.data)
	r.nextIndex++
	return nil
}

// handleResultChanClosed handles pending segments or errors after resultChan is closed.
func (r *concurrentLinkReader) handleResultChanClosed() error {
	if res, ok := r.popPendingSegment(); ok {
		return r.applySegment(res)
	}
	if r.firstErr != nil {
		r.cancel()
		return r.firstErr
	}
	return io.EOF
}

// loadNextSegment loads the next sequential segment into currBuf from pending or resultChan.
func (r *concurrentLinkReader) loadNextSegment() error {
	if r.nextIndex >= len(r.tasks) {
		return io.EOF
	}

	if res, ok := r.popPendingSegment(); ok {
		return r.applySegment(res)
	}

	for {
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case res, ok := <-r.resultChan:
			if !ok {
				return r.handleResultChanClosed()
			}
			if res.err != nil && r.firstErr == nil {
				r.firstErr = res.err
			}
			if res.index == r.nextIndex {
				return r.applySegment(res)
			}
			r.pending[res.index] = res
		}
	}
}

// Read reads downloaded segment bytes into p, loading next segments sequentially as needed.
func (r *concurrentLinkReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		if r.currBuf == nil && r.nextIndex >= len(r.tasks) {
			return 0, io.EOF
		}
		return 0, nil
	}
	for {
		if r.currBuf == nil || r.currBuf.Len() == 0 {
			if r.nextIndex >= len(r.tasks) {
				return 0, io.EOF
			}
			if err := r.loadNextSegment(); err != nil {
				return 0, err
			}
		}
		n, err := r.currBuf.Read(p)
		if err == io.EOF {
			r.currBuf = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func newLinkReader(links []ContentLink) io.ReadCloser {
	return newLinkReaderWithContext(context.Background(), defaultPooledHTTPClient, links)
}

func newLinkReaderWithContext(ctx context.Context, httpClient *http.Client, links []ContentLink) io.ReadCloser {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	if httpClient == nil || httpClient == http.DefaultClient {
		httpClient = defaultPooledHTTPClient
	}

	var tasks []segmentTask
	taskIndex := 0
	for _, link := range links {
		if link.LinkType == "" || link.LinkType == LinkTypeBase64 || link.Size == 0 {
			tasks = append(tasks, segmentTask{
				index: taskIndex,
				link:  link,
				start: 0,
				end:   -1,
			})
			taskIndex++
			continue
		}
		var offset int64
		for offset < int64(link.Size) {
			end := offset + SegmentSize - 1
			if end >= int64(link.Size) {
				end = int64(link.Size) - 1
			}
			tasks = append(tasks, segmentTask{
				index: taskIndex,
				link:  link,
				start: offset,
				end:   end,
			})
			taskIndex++
			offset = end + 1
		}
	}

	r := &concurrentLinkReader{
		ctx:        ctx,
		cancel:     cancel,
		httpClient: httpClient,
		tasks:      tasks,
		taskChan:   make(chan segmentTask, DefaultMaxConcurrentSegments),
		resultChan: make(chan segmentResult, DefaultMaxConcurrentSegments),
		closeChan:  make(chan struct{}),
		pending:    make(map[int]segmentResult),
	}

	go r.dispatcher()

	numWorkers := DefaultMaxConcurrentSegments
	if len(tasks) < numWorkers {
		numWorkers = len(tasks)
	}
	if numWorkers == 0 {
		numWorkers = 1
	}
	for i := 0; i < numWorkers; i++ {
		r.wg.Add(1)
		go r.worker()
	}

	go func() {
		r.wg.Wait()
		close(r.resultChan)
	}()

	return r
}
