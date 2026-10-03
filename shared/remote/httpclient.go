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
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/abfs/shared/entities"
)

var ErrRemoteNotSupportRange = fmt.Errorf("server does not support range requests")
var ErrInvalidRange = fmt.Errorf("invalid range")

// FetchRange downloads the content in the range [start, end) from the specified URL.
// It assumes the range is small enough to fit in memory and returning []byte makes sense.
func FetchRange(ctx context.Context, client *http.Client, urlStr string, start, end int64) (data []byte, retErr error) {
	if start < 0 || end >= 0 && end < start {
		return nil, ErrInvalidRange
	}

	if client == nil {
		client = defaultPooledHTTPClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create http request for url %s: %w", urlStr, err)
	}

	if end <= 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
	} else {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer entities.CloseErrCheck(resp.Body, &retErr)

	if resp.StatusCode == http.StatusOK {
		if start == 0 && end <= 0 {
			return io.ReadAll(resp.Body)
		}
		return nil, ErrRemoteNotSupportRange
	}

	if resp.StatusCode != http.StatusPartialContent {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		bodyText := strings.TrimSpace(string(bodyBytes))
		return nil, fmt.Errorf("unexpected http status %d for (range %d-%d): %s", resp.StatusCode, start, end, bodyText)
	}

	return io.ReadAll(resp.Body)
}

// RobustFetchRange fetches a byte range from the specified URL with retries on failure, returning the downloaded bytes.
func RobustFetchRange(ctx context.Context, client *http.Client, urlStr string, start, end int64, maxRetries int, initialBackoff time.Duration) ([]byte, error) {
	if maxRetries < 1 {
		maxRetries = 1
	}
	if initialBackoff <= 0 {
		initialBackoff = 100 * time.Millisecond
	}

	backoff := initialBackoff
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
			}
		}

		data, err := FetchRange(ctx, client, urlStr, start, end)
		if err == nil {
			return data, nil
		}

		if errors.Is(err, ErrRemoteNotSupportRange) || errors.Is(err, ErrInvalidRange) || ctx.Err() != nil {
			return nil, err
		}

		lastErr = err
	}

	return nil, lastErr
}
