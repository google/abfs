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
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/remote"
)

func TestRobustFetchRange_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "bytes=0-4", r.Header.Get("Range"))
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("hello"))
	}))
	defer ts.Close()

	data, err := remote.RobustFetchRange(context.Background(), ts.Client(), ts.URL, 0, 4, 3, 10*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}

func TestRobustFetchRange_RetryThenSuccess(t *testing.T) {
	var attempts int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("recovered"))
	}))
	defer ts.Close()

	data, err := remote.RobustFetchRange(context.Background(), ts.Client(), ts.URL, 0, 8, 3, 5*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "recovered", string(data))
	assert.Equal(t, int32(3), atomic.LoadInt32(&attempts))
}

func TestRobustFetchRange_NonRetriableRangeNotSupported(t *testing.T) {
	var attempts int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusOK) // 200 OK for partial range request -> non-retriable ErrRemoteNotSupportRange
		_, _ = w.Write([]byte("full payload"))
	}))
	defer ts.Close()

	_, err := remote.RobustFetchRange(context.Background(), ts.Client(), ts.URL, 0, 5, 3, 5*time.Millisecond)
	require.Error(t, err)
	assert.ErrorIs(t, err, remote.ErrRemoteNotSupportRange)
	assert.Equal(t, int32(1), atomic.LoadInt32(&attempts))
}

func TestRobustFetchRange_InvalidArguments(t *testing.T) {
	_, err := remote.RobustFetchRange(context.Background(), nil, "http://example.com", -1, 5, 3, 5*time.Millisecond)
	require.ErrorIs(t, err, remote.ErrInvalidRange)

	_, err = remote.RobustFetchRange(context.Background(), nil, "http://example.com", 10, 5, 3, 5*time.Millisecond)
	require.ErrorIs(t, err, remote.ErrInvalidRange)
}

func TestRobustFetchRange_ContextCancelled(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := remote.RobustFetchRange(ctx, ts.Client(), ts.URL, 0, 5, 10, 50*time.Millisecond)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
