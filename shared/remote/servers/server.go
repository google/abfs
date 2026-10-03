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

package servers

import (
	"io"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/store/localcache"
	"github.com/google/abfs/shared/zstd"
)

type Hash = entities.Hash

const ServerVersion = "0.1.1"
const ClientMinVersion = "0.1.2"

type ZstdCompressor struct{}

var _ localcache.Compressor = (*ZstdCompressor)(nil)

func (z *ZstdCompressor) Compress(buf []byte) ([]byte, error) {
	return zstd.Compress(buf)
}

func (*ZstdCompressor) Decompress(buf []byte) ([]byte, error) {
	return zstd.Decompress(buf)
}

func (*ZstdCompressor) NewReader(r io.Reader) io.ReadCloser {
	return zstd.NewReader(r)
}

type ServerOpt interface{}
type New func(opts ...ServerOpt) (remote.RemoteCloser, error)

type NoLinkValidation struct {
	Value bool
}

func NoLinkValidationOpt(value bool) ServerOpt {
	return NoLinkValidation{Value: value}
}

type NoHashValidation struct {
	Value bool
}

func NoHashValidationOpt(value bool) ServerOpt {
	return NoHashValidation{Value: value}
}
