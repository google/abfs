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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
	_ "github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/zstd"
)

func TestValidatorEmptyDir(t *testing.T) {
	validator, err := NewValidator(entities.EmptyDirHash, EncodingRaw)
	require.NoError(t, err)
	_, err = validator.Write(entities.EmptyDirContent)
	require.NoError(t, err)
	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, entities.ZeroHash, validator.Hash())
	require.Equal(t, "tree", validator.Type())
	require.Equal(t, int64(0), validator.Size())
}

func TestValidatorEmptyDirLinks(t *testing.T) {
	validator, err := NewValidator(entities.EmptyDirHash, EncodingRaw, ExtractLinksOpt(true))
	require.NoError(t, err)
	_, err = validator.Write(entities.EmptyDirContent)
	require.NoError(t, err)
	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, entities.ZeroHash, validator.Hash())
	require.Equal(t, "tree", validator.Type())
	require.Equal(t, int64(0), validator.Size())
	require.Equal(t, []Hash{}, validator.Links())
}

func TestValidatorDirLinksExist(t *testing.T) {
	validator, err := NewValidator(entities.NewHash("a48ac58b824df793b8e47a944cae47b4a1904ded"), EncodingRaw, ExtractLinksOpt(true))
	require.NoError(t, err)

	_, err = validator.Write([]byte("tree 46\x00100644 blob hello world!\n\x00"))
	require.NoError(t, err)
	blobHash := entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	_, err = validator.Write(blobHash[:])
	require.NoError(t, err)

	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, entities.ZeroHash, validator.Hash())
	require.Equal(t, "tree", validator.Type())
	require.Equal(t, int64(46), validator.Size())
	require.Equal(t, []Hash{blobHash}, validator.Links())
}

func TestValidatorEmptyBlob(t *testing.T) {
	validator, err := NewValidator(entities.EmptyFileHash, EncodingRaw)
	require.NoError(t, err)
	_, err = validator.Write([]byte("blob 0\x00"))
	require.NoError(t, err)
	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, entities.ZeroHash, validator.Hash())
	require.Equal(t, "blob", validator.Type())
	require.Equal(t, int64(0), validator.Size())
}

func TestValidatorSmallBlob(t *testing.T) {
	validator, err := NewValidator(entities.NewHash("a0423896973644771497bdc03eb99d5281615b51"), EncodingRaw)
	require.NoError(t, err)
	_, err = validator.Write([]byte("blob 13\x00hello world!\n"))
	require.NoError(t, err)
	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, entities.ZeroHash, validator.Hash())
	require.Equal(t, "blob", validator.Type())
	require.Equal(t, int64(13), validator.Size())
}

func TestValidatorBigBlob(t *testing.T) {
	validator, err := NewValidator(entities.NewHash("af54068976194b1c3bd6c1298480726d16633289"), EncodingRaw)
	require.NoError(t, err)

	_, err = fmt.Fprintf(validator, "blob %d\x00", 130000)
	require.NoError(t, err)
	_, err = validator.Write(bytes.Repeat([]byte("hello world!\n"), 10000))
	require.NoError(t, err)
	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, entities.ZeroHash, validator.Hash())
	require.Equal(t, "blob", validator.Type())
	require.Equal(t, int64(130000), validator.Size())
}

func TestValidatorEmptyDirHash(t *testing.T) {
	validator, err := NewValidator(entities.EmptyDirHash, EncodingRaw, ValidateHashOpt(true))
	require.NoError(t, err)
	_, err = validator.Write(entities.EmptyDirContent)
	require.NoError(t, err)
	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, entities.EmptyDirHash, validator.Hash())
	require.Equal(t, "tree", validator.Type())
	require.Equal(t, int64(0), validator.Size())
}

func TestValidatorEmptyBlobHash(t *testing.T) {
	validator, err := NewValidator(entities.EmptyFileHash, EncodingRaw, ValidateHashOpt(true))
	require.NoError(t, err)
	_, err = validator.Write([]byte("blob 0\x00"))
	require.NoError(t, err)
	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, entities.EmptyFileHash, validator.Hash())
	require.Equal(t, "blob", validator.Type())
	require.Equal(t, int64(0), validator.Size())
}

func TestValidatorSmallBlobHash(t *testing.T) {
	validator, err := NewValidator(entities.NewHash("a0423896973644771497bdc03eb99d5281615b51"), EncodingRaw)
	require.NoError(t, err)
	_, err = validator.Write([]byte("blob 13\x00hello world!\n"))
	require.NoError(t, err)
	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, "blob", validator.Type())
	require.Equal(t, int64(13), validator.Size())
}

func TestValidatorBigBlobHash(t *testing.T) {
	validator, err := NewValidator(entities.NewHash("af54068976194b1c3bd6c1298480726d16633289"), EncodingRaw, ValidateHashOpt(true))
	require.NoError(t, err)

	_, err = fmt.Fprintf(validator, "blob %d\x00", 130000)
	require.NoError(t, err)
	_, err = validator.Write(bytes.Repeat([]byte("hello world!\n"), 10000))
	require.NoError(t, err)
	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, "blob", validator.Type())
	require.Equal(t, int64(130000), validator.Size())
}

func TestValidatorEmptyDirLinksHash(t *testing.T) {
	validator, err := NewValidator(entities.EmptyDirHash, EncodingRaw, ExtractLinksOpt(true), ValidateHashOpt(true))
	require.NoError(t, err)
	_, err = validator.Write(entities.EmptyDirContent)
	require.NoError(t, err)
	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, "tree", validator.Type())
	require.Equal(t, int64(0), validator.Size())
	require.Equal(t, []Hash{}, validator.Links())
}

func TestValidatorDirLinksExistHash(t *testing.T) {
	validator, err := NewValidator(entities.NewHash("a48ac58b824df793b8e47a944cae47b4a1904ded"), EncodingRaw, ExtractLinksOpt(true), ValidateHashOpt(true))
	require.NoError(t, err)

	_, err = validator.Write([]byte("tree 46\x00100644 blob hello world!\n\x00"))
	require.NoError(t, err)
	blobHash := entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	_, err = validator.Write(blobHash[:])
	require.NoError(t, err)

	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, "tree", validator.Type())
	require.Equal(t, int64(46), validator.Size())
	require.Equal(t, []Hash{blobHash}, validator.Links())
}

func TestValidatorZstdEmptyDir(t *testing.T) {
	validator, err := NewValidator(entities.EmptyDirHash, EncodingZStd)
	require.NoError(t, err)

	zbytes, err := zstd.Compress(entities.EmptyDirContent)
	require.NoError(t, err)
	_, err = validator.Write(zbytes)
	require.NoError(t, err)

	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, entities.ZeroHash, validator.Hash())
	require.Equal(t, "tree", validator.Type())
	require.Equal(t, int64(0), validator.Size())
}

func TestValidatorZstdEmptyBlob(t *testing.T) {
	validator, err := NewValidator(entities.EmptyFileHash, EncodingZStd)
	require.NoError(t, err)

	zbytes, err := zstd.Compress([]byte("blob 0\x00"))
	require.NoError(t, err)
	_, err = validator.Write(zbytes)
	require.NoError(t, err)

	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, entities.ZeroHash, validator.Hash())
	require.Equal(t, "blob", validator.Type())
	require.Equal(t, int64(0), validator.Size())
}

func TestValidatorZstdSmallBlob(t *testing.T) {
	validator, err := NewValidator(entities.NewHash("a0423896973644771497bdc03eb99d5281615b51"), EncodingZStd)
	require.NoError(t, err)

	zbytes, err := zstd.Compress([]byte("blob 13\x00hello world!\n"))
	require.NoError(t, err)
	_, err = validator.Write(zbytes)
	require.NoError(t, err)

	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, entities.ZeroHash, validator.Hash())
	require.Equal(t, "blob", validator.Type())
	require.Equal(t, int64(13), validator.Size())
}

func TestValidatorZstdBigBlob(t *testing.T) {
	validator, err := NewValidator(entities.NewHash("af54068976194b1c3bd6c1298480726d16633289"), EncodingZStd)
	require.NoError(t, err)

	zbytes, err := zstd.Compress(append([]byte("blob 130000\x00"), bytes.Repeat([]byte("hello world!\n"), 10000)...))
	require.NoError(t, err)
	_, err = validator.Write(zbytes)
	require.NoError(t, err)

	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, entities.ZeroHash, validator.Hash())
	require.Equal(t, "blob", validator.Type())
	require.Equal(t, int64(130000), validator.Size())
}

func TestValidatorZstdEmptyDirHash(t *testing.T) {
	validator, err := NewValidator(entities.EmptyDirHash, EncodingZStd, ValidateHashOpt(true))
	require.NoError(t, err)

	zbytes, err := zstd.Compress(entities.EmptyDirContent)
	require.NoError(t, err)
	_, err = validator.Write(zbytes)
	require.NoError(t, err)

	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, entities.EmptyDirHash, validator.Hash())
	require.Equal(t, "tree", validator.Type())
	require.Equal(t, int64(0), validator.Size())
}

func TestValidatorZstdEmptyBlobHash(t *testing.T) {
	validator, err := NewValidator(entities.EmptyFileHash, EncodingZStd, ValidateHashOpt(true))
	require.NoError(t, err)

	zbytes, err := zstd.Compress([]byte("blob 0\x00"))
	require.NoError(t, err)
	_, err = validator.Write(zbytes)
	require.NoError(t, err)

	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, entities.EmptyFileHash, validator.Hash())
	require.Equal(t, "blob", validator.Type())
	require.Equal(t, int64(0), validator.Size())
}

func TestValidatorZstdSmallBlobHash(t *testing.T) {
	validator, err := NewValidator(entities.NewHash("a0423896973644771497bdc03eb99d5281615b51"), EncodingZStd)
	require.NoError(t, err)

	zbytes, err := zstd.Compress([]byte("blob 13\x00hello world!\n"))
	require.NoError(t, err)
	_, err = validator.Write(zbytes)
	require.NoError(t, err)

	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, "blob", validator.Type())
	require.Equal(t, int64(13), validator.Size())
}

func TestValidatorZstdBigBlobHash(t *testing.T) {
	validator, err := NewValidator(entities.NewHash("af54068976194b1c3bd6c1298480726d16633289"), EncodingZStd, ValidateHashOpt(true))
	require.NoError(t, err)

	zbytes, err := zstd.Compress(append([]byte("blob 130000\x00"), bytes.Repeat([]byte("hello world!\n"), 10000)...))
	require.NoError(t, err)
	_, err = validator.Write(zbytes)
	require.NoError(t, err)

	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, "blob", validator.Type())
	require.Equal(t, int64(130000), validator.Size())
}

func TestValidatorZstdEmptyDirLinksHash(t *testing.T) {
	validator, err := NewValidator(entities.EmptyDirHash, EncodingZStd, ExtractLinksOpt(true), ValidateHashOpt(true))
	require.NoError(t, err)

	zbytes, err := zstd.Compress(entities.EmptyDirContent)
	require.NoError(t, err)
	_, err = validator.Write(zbytes)
	require.NoError(t, err)

	require.NoError(t, err)
	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, "tree", validator.Type())
	require.Equal(t, int64(0), validator.Size())
	require.Equal(t, []Hash{}, validator.Links())
}

func TestValidatorZstdDirLinksExistHash(t *testing.T) {
	validator, err := NewValidator(entities.NewHash("a48ac58b824df793b8e47a944cae47b4a1904ded"), EncodingZStd, ExtractLinksOpt(true), ValidateHashOpt(true))
	require.NoError(t, err)

	blobHash := entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	zbytes, err := zstd.Compress(append([]byte("tree 46\x00100644 blob hello world!\n\x00"), blobHash[:]...))
	require.NoError(t, err)
	_, err = validator.Write(zbytes)
	require.NoError(t, err)

	err = validator.Close()
	require.NoError(t, err)
	require.Equal(t, "tree", validator.Type())
	require.Equal(t, int64(46), validator.Size())
	require.Equal(t, []Hash{blobHash}, validator.Links())
}
