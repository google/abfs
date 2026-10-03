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
	"io"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

type ContentReader struct {
	reader   io.Reader
	close    func() error
	encoding string
}

var _ ContentReadCloser = (*ContentReader)(nil)

func (r *ContentReader) Read(p []byte) (n int, err error) {
	return r.reader.Read(p)
}

func (r *ContentReader) Close() error {
	if r.close == nil {
		return nil
	}
	return r.close()
}

func (r *ContentReader) Encoding() string {
	return r.encoding
}

type errReader struct {
	err error
}

func (r errReader) Read([]byte) (int, error) {
	return 0, r.err
}

// BundledReader implements BundledReadCloser by wrapping a ContentReadCloser
// and carrying a map of extra objects discovered during a fetch.
type BundledReader struct {
	ContentReadCloser
	blobs      []BundledObject
	objects    []BundledObject
	mapResults []BundledMapResult
}

func (r *BundledReader) Blobs() []BundledObject {
	return r.blobs
}

func (r *BundledReader) Objects() []BundledObject {
	return r.objects
}

func (r *BundledReader) MapResults() []BundledMapResult {
	return r.mapResults
}

// NewBundledReader creates a reader that includes additional side-car objects.
func NewBundledReader(reader ContentReadCloser, blobs []BundledObject, objects []BundledObject, mapResults []BundledMapResult) *BundledReader {
	return &BundledReader{
		ContentReadCloser: reader,
		blobs:             blobs,
		objects:           objects,
		mapResults:        mapResults,
	}
}

func NewContentReader(reader io.ReadCloser, encoding string) *ContentReader {
	return &ContentReader{
		reader:   reader,
		close:    reader.Close,
		encoding: encoding,
	}
}
func NewContentReaderWithCustomClose(reader ContentReadCloser, close func() error) *ContentReader {
	return &ContentReader{
		reader:   reader,
		close:    close,
		encoding: reader.Encoding(),
	}
}

func NewBlobReader(reader io.ReadCloser, size int64) *ContentReader {
	return &ContentReader{
		reader:   objects.PrependHeaderReader(reader, "blob", size),
		close:    reader.Close,
		encoding: EncodingRaw,
	}
}

func NewBlobReaderFromBytes(content []byte) *ContentReader {
	return NewBlobReader(io.NopCloser(bytes.NewReader(content)), int64(len(content)))
}

func NewReaderFromBytes(content []byte) *ContentReader {
	return &ContentReader{
		reader:   bytes.NewReader(content),
		close:    nil,
		encoding: EncodingRaw,
	}
}

func ContentReaderFromBytes(data []byte) *ContentReader {
	return &ContentReader{
		reader:   bytes.NewReader(data),
		encoding: EncodingRaw,
	}
}

func FetchBlob(ctx context.Context, remote RemoteProvider, hash Hash) (content []byte, retErr error) {
	reader, err := remote.GetObject(ctx, hash)
	if err != nil {
		return nil, err
	}
	defer entities.CloseErrCheck(reader, &retErr)

	return io.ReadAll(reader)
}

func FetchObject[T any](ctx context.Context, remote RemoteProvider, hash Hash) (*T, error) {
	content, err := FetchBlob(ctx, remote, hash)
	if err != nil {
		return nil, err
	}

	var r T
	err = any(&r).(objects.Object).Decode(hash, content)
	return &r, err
}
