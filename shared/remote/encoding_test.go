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

package remote_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/zstd"
)

func TestLinkReaderBase64(t *testing.T) {
	links := []remote.ContentLink{
		{LinkType: remote.LinkTypeBase64, Content: "SGVsbG8sIA=="},
		{LinkType: remote.LinkTypeBase64, Content: "V29ybGQh"},
	}
	linksContent := remote.LinksContent{
		Encoding: remote.EncodingRaw,
		Links:    links,
	}
	buf, err := json.Marshal(linksContent)
	require.NoError(t, err)

	reader := remote.NewLinkReader(io.NopCloser(bytes.NewReader(buf)))
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "Hello, World!", string(data))
	require.NoError(t, reader.Close())
}

func TestLinkReaderURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/part1":
			_, _ = w.Write([]byte("foo-"))
		case "/part2":
			_, _ = w.Write([]byte("bar"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	links := []remote.ContentLink{
		{LinkType: remote.LinkTypeURL, Content: ts.URL + "/part1"},
		{LinkType: remote.LinkTypeURL, Content: ts.URL + "/part2"},
	}
	linksContent := remote.LinksContent{
		Encoding: remote.EncodingRaw,
		Links:    links,
	}
	buf, err := json.Marshal(linksContent)
	require.NoError(t, err)

	reader := remote.NewLinkReaderWithContext(context.Background(), io.NopCloser(bytes.NewReader(buf)), ts.Client())
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "foo-bar", string(data))
	require.NoError(t, reader.Close())
}

func TestLinkReaderMixedAndClose(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("from-url"))
	}))
	defer ts.Close()

	links := []remote.ContentLink{
		{LinkType: remote.LinkTypeBase64, Content: "cmF3MS0="},
		{LinkType: remote.LinkTypeURL, Content: ts.URL + "/something"},
		{LinkType: remote.LinkTypeBase64, Content: "LXJhdzI="},
	}
	linksContent := remote.LinksContent{
		Encoding: remote.EncodingRaw,
		Links:    links,
	}
	buf, err := json.Marshal(linksContent)
	require.NoError(t, err)

	reader := remote.NewLinkReaderWithContext(context.Background(), io.NopCloser(bytes.NewReader(buf)), ts.Client())

	// Read first few bytes
	p := make([]byte, 3)
	n, err := reader.Read(p)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, "raw", string(p[:n]))

	// Close prematurely
	require.NoError(t, reader.Close())

	// Further read after Close should return io.EOF
	n, err = reader.Read(p)
	assert.Equal(t, 0, n)
	assert.Equal(t, io.EOF, err)
}

func TestLinkReaderZStd(t *testing.T) {
	original := "Hello, Zstd compressed linked content!"
	compressed, err := zstd.Compress([]byte(original))
	require.NoError(t, err)

	mid := len(compressed) / 2
	links := []remote.ContentLink{
		{LinkType: remote.LinkTypeBase64, Content: base64.StdEncoding.EncodeToString(compressed[:mid])},
		{LinkType: remote.LinkTypeBase64, Content: base64.StdEncoding.EncodeToString(compressed[mid:])},
	}
	linksContent := remote.LinksContent{
		Encoding: remote.EncodingZStd,
		Links:    links,
	}
	buf, err := json.Marshal(linksContent)
	require.NoError(t, err)

	reader := remote.NewLinkReader(io.NopCloser(bytes.NewReader(buf)))
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, original, string(data))
	require.NoError(t, reader.Close())
}

func TestLinkReaderRangeSegments(t *testing.T) {
	remote.SetSegmentSize(10) // 10 bytes per range segment
	defer remote.SetSegmentSize(16 * 1024 * 1024)

	payload := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ" // 62 bytes total -> 7 segments

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHdr := r.Header.Get("Range")
		if rangeHdr == "" {
			_, _ = w.Write([]byte(payload))
			return
		}
		// Parse bytes=start-end
		parts := strings.Split(strings.TrimPrefix(rangeHdr, "bytes="), "-")
		start, _ := strconv.Atoi(parts[0])
		end, _ := strconv.Atoi(parts[1])
		if end >= len(payload) {
			end = len(payload) - 1
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(payload[start : end+1]))
	}))
	defer ts.Close()

	links := []remote.ContentLink{
		{LinkType: remote.LinkTypeURL, Size: uint64(len(payload)), Content: ts.URL + "/largeblob"},
	}
	linksContent := remote.LinksContent{
		Encoding: remote.EncodingRaw,
		Links:    links,
	}
	buf, err := json.Marshal(linksContent)
	require.NoError(t, err)

	reader := remote.NewLinkReaderWithContext(context.Background(), io.NopCloser(bytes.NewReader(buf)), ts.Client())
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, payload, string(data))
	require.NoError(t, reader.Close())
}

func TestLinkReaderURLRetries(t *testing.T) {
	var attempts int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable) // 503 Service Unavailable for first 2 attempts
			return
		}
		_, _ = w.Write([]byte("retried-successfully"))
	}))
	defer ts.Close()

	links := []remote.ContentLink{
		{LinkType: remote.LinkTypeURL, Content: ts.URL + "/flaky-url"},
	}
	linksContent := remote.LinksContent{
		Encoding: remote.EncodingRaw,
		Links:    links,
	}
	buf, err := json.Marshal(linksContent)
	require.NoError(t, err)

	reader := remote.NewLinkReaderWithContext(context.Background(), io.NopCloser(bytes.NewReader(buf)), ts.Client())
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "retried-successfully", string(data))
	assert.GreaterOrEqual(t, atomic.LoadInt32(&attempts), int32(3))
	require.NoError(t, reader.Close())
}

func TestLinkReaderError(t *testing.T) {
	reader := remote.NewLinkReader(io.NopCloser(strings.NewReader("invalid json")))
	p := make([]byte, 10)
	n, err := reader.Read(p)
	assert.Equal(t, 0, n)
	assert.Error(t, err)
	require.NoError(t, reader.Close())
}

func TestLinkReaderRangeNotSupported(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Server ignores Range header and returns 200 OK
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("entire-file-payload"))
	}))
	defer ts.Close()

	links := []remote.ContentLink{
		{LinkType: remote.LinkTypeURL, Size: 100, Content: ts.URL + "/norange"},
	}
	linksContent := remote.LinksContent{
		Encoding: remote.EncodingRaw,
		Links:    links,
	}
	buf, err := json.Marshal(linksContent)
	require.NoError(t, err)

	reader := remote.NewLinkReaderWithContext(context.Background(), io.NopCloser(bytes.NewReader(buf)), ts.Client())
	p := make([]byte, 100)
	_, err = reader.Read(p)
	assert.ErrorContains(t, err, "server does not support range requests")
	require.NoError(t, reader.Close())
}

func TestLinkReaderURLMidStreamRetry(t *testing.T) {
	payload := "0123456789ABCDEFGHIJ" // 20 bytes payload
	var attempts int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		rangeHdr := r.Header.Get("Range")
		t.Logf("Attempt #%d: received Range=%q", count, rangeHdr)

		if count == 1 {
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, bufrw, err := hj.Hijack()
				if err == nil {
					_, _ = bufrw.WriteString("HTTP/1.1 206 Partial Content\r\nContent-Type: text/plain\r\nContent-Length: 20\r\n\r\n")
					_, _ = bufrw.WriteString(payload[:10])
					_ = bufrw.Flush()
					time.Sleep(20 * time.Millisecond)
					_ = conn.Close()
					return
				}
			}
			return
		}

		// Subsequent retry: verify request asks for full range bytes=0-19
		assert.Equal(t, "bytes=0-19", rangeHdr)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(payload))
	}))
	defer ts.Close()

	links := []remote.ContentLink{
		{LinkType: remote.LinkTypeURL, Size: uint64(len(payload)), Content: ts.URL + "/midstream-flaky"},
	}
	linksContent := remote.LinksContent{
		Encoding: remote.EncodingRaw,
		Links:    links,
	}
	buf, err := json.Marshal(linksContent)
	require.NoError(t, err)

	reader := remote.NewLinkReaderWithContext(context.Background(), io.NopCloser(bytes.NewReader(buf)), ts.Client())
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, payload, string(data))
	assert.Equal(t, int32(2), atomic.LoadInt32(&attempts))
	require.NoError(t, reader.Close())
}
