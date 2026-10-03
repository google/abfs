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

package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/objects"
	_ "github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/grpc/server"
	"github.com/google/abfs/shared/remote/servers/inmemory"
)

// Setup creates a server running on the local machine and a client connected to it.
// It returns the client and a cleanup function.
func Setup(t *testing.T) (*ABFSClient, func()) {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", 0))
	if err != nil {
		t.Fatalf("failed to listen: %v\n", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	fserver := inmemory.New()

	s, err := server.NewGRPCServer(&server.GRPCServerOpts{Server: fserver})
	if err != nil {
		t.Fatalf("failed to create grpc server %v", err)
	}
	go func() {
		if err := s.Serve(lis); err != nil {
			_ = fserver.Close()
			t.Errorf("Failed to serve the ABFS Server: %v\n", err)
		}
	}()
	c, err := NewABFSClient(&ABFSClientOpts{Servers: []string{fmt.Sprintf("localhost:%d", port)}, AuthInfo: instance.CreateAuthInfo(instance.AuthTypeNone, "", "", true)})
	if err != nil {
		s.GracefulStop()
		_ = fserver.Close()
		t.Fatalf("did not connect: %v\n", err)
	}
	closeFunc := func() {
		if err := c.Close(); err != nil {
			t.Logf("Failed to close the client: %v\n", err)
		}
		s.GracefulStop()
		// lis is closed by the gRPC server
		if err := fserver.Close(); err != nil {
			t.Logf("Failed to close the server: %v\n", err)
		}
	}
	return c, closeFunc
}

// TestSetup will tell us if there is a problem with our Setup test code
func TestSetup(t *testing.T) {
	_, close := Setup(t)
	close()
}

func TestEmptyFile(t *testing.T) {
	require := require.New(t)
	c, close := Setup(t)
	defer close()

	reader, err := c.GetObject(context.Background(), entities.EmptyFileHash)
	require.NoError(err, "failed to get")

	blob, err := objects.ReadBlob(reader)
	require.NoError(err)
	require.Empty(blob)
}

// TestEmptyDir catches a previous bug where we seeded the cache with a bad
// value
func TestEmptyDir(t *testing.T) {
	require := require.New(t)
	c, close := Setup(t)
	defer close()

	reader, err := c.GetObject(context.Background(), entities.EmptyDirHash)
	require.NoError(err, "failed to get")

	obj, err := io.ReadAll(reader)
	require.NoError(err, "failed to read")
	require.Equal(entities.EmptyDirContent, obj)
}

func TestPutAndGetBlob(t *testing.T) {
	require := require.New(t)
	c, close := Setup(t)
	defer close()
	ctx := context.Background()

	expectedData := []byte("This is some\nTest Data\n")
	testKey := objects.HashBlobBytes(expectedData)
	err := c.PutObject(ctx, testKey, remote.NewBlobReaderFromBytes(expectedData))
	require.NoError(err, "failed to put")

	reader, err := c.GetObject(ctx, testKey)
	require.NoError(err, "failed to get")

	actualData, err := objects.ReadBlob(reader)
	require.NoError(err, "failed to read")
	require.Equal(expectedData, actualData, "Get data %q did not match Put data %q", actualData, expectedData)
}

func TestPutAndGetBlobZstd(t *testing.T) {
	require := require.New(t)
	c, close := Setup(t)
	defer close()
	ctx := context.Background()

	expectedData := []byte("This is some\nTest Data\n")
	testKey := objects.HashBlobBytes(expectedData)

	err := c.PutObject(ctx, testKey, remote.NewZStdContentReader(fmt.Appendf(nil, "blob %d\x00%s", len(expectedData), expectedData)))
	require.NoError(err, "failed to put")

	reader, err := c.GetObject(ctx, testKey)
	require.NoError(err, "failed to get")

	actualData, err := objects.ReadBlob(reader)
	require.NoError(err, "failed to read")
	require.Equal(expectedData, actualData, "Get data %q did not match Put data %q", actualData, expectedData)
}

func TestPutAndGetBlobZstdHuge(t *testing.T) {
	require := require.New(t)
	c, close := Setup(t)
	defer close()
	ctx := context.Background()

	expectedData := bytes.Repeat([]byte("This is some\nTest Data\n"), 1000000)
	testKey := objects.HashBlobBytes(expectedData)

	err := c.PutObject(ctx, testKey, remote.NewZStdContentReader(fmt.Appendf(nil, "blob %d\x00%s", len(expectedData), expectedData)))
	require.NoError(err, "failed to put")

	err = c.PutObject(ctx, testKey, remote.NewZStdContentReader(fmt.Appendf(nil, "blob %d\x00%s", len(expectedData), expectedData)))
	require.NoError(err, "failed to re-put")

	reader, err := c.GetObject(ctx, testKey)
	require.NoError(err, "failed to get")

	actualData, err := objects.ReadBlob(reader)
	require.NoError(err, "failed to read")
	require.Equal(expectedData, actualData, "Get data %q did not match Put data %q", actualData, expectedData)
}

func TestHeadersSimple(t *testing.T) {
	require := require.New(t)
	c, close := Setup(t)
	defer close()
	ctx := context.Background()

	expectedData := []byte("This is some\nTest Data\n")
	testKey := objects.HashBlobBytes(expectedData)

	err := c.PutObject(ctx, testKey, remote.NewZStdContentReader(fmt.Appendf(nil, "blob %d\x00%s", len(expectedData), expectedData)))
	require.NoError(err, "failed to put")

	headers, err := c.Headers(ctx, []Hash{
		testKey,
		entities.EmptyDirHash,
		entities.EmptyFileHash,
		entities.NewHash("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"),
	})
	require.NoError(err, "failed to get headers")
	require.Equal([]entities.ObjectHeader{
		{Type: "blob", Size: 23},
		{Type: "tree", Size: 0},
		{Type: "blob", Size: 0},
		{Type: "", Size: 0},
	}, headers)
}

type TestLinkedObject struct {
	objects.JSONBaseObject
	Link Hash
}

func (l *TestLinkedObject) Decode(hash objects.Hash, content []byte) error {
	return objects.ObjectJSONDecode(l, "TestLinkedObject", hash, content)
}
func (l *TestLinkedObject) Encode() error {
	return objects.ObjectJSONEncode(l, "TestLinkedObject")
}
func (l *TestLinkedObject) Links() []Hash {
	return []Hash{l.Link}
}

func init() {
	objects.Factory.Register("TestLinkedObject", func() objects.Object {
		return new(TestLinkedObject)
	})
}

func TestPutObjectMissingLink(t *testing.T) {
	require := require.New(t)
	c, close := Setup(t)
	defer close()
	ctx := context.Background()

	missingHash := entities.NewHash("001268641110c8de68d2b238897d156536dd9e42")

	testObj := TestLinkedObject{
		Link: missingHash,
	}

	require.NoError(testObj.Encode(), "failed to encode")

	err := c.PutObject(ctx, testObj.Hash, remote.NewReaderFromBytes(testObj.Encoded))
	require.Error(err, "expecting validation error")

	var e *remote.ErrObjectLinkValidation
	if errors.As(err, &e) {
		require.Len(e.MissingLinks, 1, "expected 1 missing link")
		require.Equal(missingHash, e.MissingLinks[0])
	} else {
		require.Fail("expected %v to have a type of remote.ErrObjectLinkValidation", err)
	}
}

// TestPutObjectMissingZeroHashLink is preventing a bug in new Link code where we might accidentally
// have a link to a ZeroHash. The server should ignore this link.
func TestPutObjectMissingZeroHashLink(t *testing.T) {
	require := require.New(t)
	c, close := Setup(t)
	defer close()
	ctx := context.Background()

	testObj := TestLinkedObject{
		Link: entities.ZeroHash,
	}

	require.NoError(testObj.Encode(), "failed to encode")

	err := c.PutObject(ctx, testObj.Hash, remote.NewReaderFromBytes(testObj.Encoded))
	require.NoError(err, "expecting no validation error")
}

func TestAddMapResult_BrokenLinkRejected(t *testing.T) {
	require := require.New(t)
	c, close := Setup(t)
	defer close()
	ctx := context.Background()

	h1 := entities.NewHash("face059aa9800b74775cadf402fc31b53c104321")
	h2 := entities.NewHash("001268641110c8de68d2b238897d156536dd9e42")

	err := c.AddMapResult(ctx, h1, h2)
	require.Error(err, "expected error from add map result")

	var e *remote.ErrObjectLinkValidation
	if errors.As(err, &e) {
		require.Len(e.MissingLinks, 2, "expected 2 missing links")
		require.Contains(e.MissingLinks, h1)
		require.Contains(e.MissingLinks, h2)
	} else {
		require.Fail("expected %v to have a type of remote.ErrObjectLinkValidation", err)
	}
}

func TestMapResultsEmptyResults(t *testing.T) {
	require := require.New(t)
	c, close := Setup(t)
	defer close()
	ctx := context.Background()

	results, err := c.MapResults(ctx, entities.NewHash("001268641110c8de68d2b238897d156536dd9e42"))
	require.NoError(err, "Failed to retrieve map results")
	require.Empty(results, "Results should be empty")
}

func TestAddAndMapResultSingle(t *testing.T) {
	require := require.New(t)
	c, close := Setup(t)
	defer close()
	ctx := context.Background()

	require.NoError(c.AddMapResult(ctx, entities.EmptyDirHash, entities.EmptyDirHash), "Failed to add map result")

	results, err := c.MapResults(ctx, entities.EmptyDirHash)
	require.NoError(err, "Failed to retrieve map results with retrying")
	require.ElementsMatch([]Hash{entities.EmptyDirHash}, results, "Retrieved hashes did not match single expected hash")
}

func TestAddAndMapResultMultiple(t *testing.T) {
	t.Skip("need to update with objects that exist")

	require := require.New(t)
	c, close := Setup(t)
	defer close()
	ctx := context.Background()

	from := entities.NewHash("001268641110c8de68d2b238897d156536dd9e42")
	expected := make([]Hash, 100)
	for i := 0; i < len(expected); i++ {
		expected[i] = objects.HashBytes(fmt.Appendf(nil, "test-%d", i))
	}
	var wg sync.WaitGroup
	wg.Add(len(expected))
	for i, h := range expected {
		go func(i int, h Hash) {
			defer wg.Done()
			require.NoError(c.AddMapResult(ctx, from, h), "Failed to add map result from %q to %q", from, h)
		}(i, h)
	}
	wg.Wait()

	var results []Hash
	results, err := c.MapResults(ctx, from)
	require.NoError(err, "Failed to get MapResults")
	if len(results) < len(expected) {
		require.Fail("didn't get enough results, so retry")
	}
	require.NoError(err, "Failed to retrieve map results with retrying")
	require.ElementsMatch(expected, results, "Retrieved hashes did not match multiple expected hashes")
}

func TestHasObjectsOverMax(t *testing.T) {
	require := require.New(t)
	c, close := Setup(t)
	defer close()
	ctx := context.Background()

	hashes := make([]entities.Hash, 1000000)

	headers, err := c.Headers(ctx, hashes)
	require.NoError(err)
	require.Len(headers, 1000000)
}

func TestPutRefs_PutAndGet(t *testing.T) {
	require := require.New(t)

	c, close := Setup(t)
	defer close()

	ref := "refs/heads/fake/long/ref/name/with/some/extra/stuff"

	err := c.PutRefs(context.Background(), "fakeserver", "fakeproject", []entities.RefHash{{
		Ref:  ref,
		Hash: entities.EmptyDirHash,
	}})
	require.NoError(err, "expect no error from PutRefs call")

	hashes, err := c.GetRefs(context.Background(), "fakeserver", "fakeproject", []string{ref})
	require.NoError(err, "expect no error from GetRefs call")
	require.Len(hashes, 1, "expected 1 ref returned")
	require.Equal(entities.EmptyDirHash, hashes[0], "expected provided hash to be returned for ref")
}

func TestPutRefs_SoftDelete(t *testing.T) {
	require := require.New(t)

	c, close := Setup(t)
	defer close()

	ref := "refs/heads/fake/long/ref/name/with/some/extra/stuff"

	err := c.PutRefs(context.Background(), "fakeserver", "fakeproject", []entities.RefHash{{
		Ref:  ref,
		Hash: entities.EmptyDirHash,
	}})
	require.NoError(err, "expect no error from PutRefs call")

	hashes, err := c.GetRefs(context.Background(), "fakeserver", "fakeproject", []string{ref})
	require.NoError(err, "expect no error from GetRefs call")
	require.Len(hashes, 1, "expected 1 ref returned")
	require.Equal(entities.EmptyDirHash, hashes[0], "expected provided hash to be returned for ref")

	err = c.PutRefs(context.Background(), "fakeserver", "fakeproject", []entities.RefHash{{
		Ref:  ref,
		Hash: entities.ZeroHash,
	}})
	require.NoError(err, "expect no error from PutRefs call")

	hashes, err = c.GetRefs(context.Background(), "fakeserver", "fakeproject", []string{ref})
	require.NoError(err, "expect no error from GetRefs call")
	require.Len(hashes, 1, "expected 1 ref returned")
	require.Equal(entities.ZeroHash, hashes[0], "expected provided hash to be returned for ref")
}

func TestPutRefs_OverMax(t *testing.T) {
	require := require.New(t)

	c, close := Setup(t)
	defer close()

	refs := make([]entities.RefHash, 100000)
	for i := range refs {
		refs[i] = entities.RefHash{
			Ref:  "refs/heads/fake/long/ref/name/with/some/extra/stuff",
			Hash: entities.EmptyDirHash,
		}
	}
	err := c.PutRefs(context.Background(), "fakeserver", "fakeproject", refs)

	require.NoError(err, "expect no error from PutRefs call")
}

func TestPutRefs_MissingLink(t *testing.T) {
	require := require.New(t)
	c, close := Setup(t)
	defer close()

	h1 := entities.NewHash("face059aa9800b74775cadf402fc31b53c104321")
	h2 := entities.NewHash("001268641110c8de68d2b238897d156536dd9e42")

	refs := []entities.RefHash{
		{
			Ref:  "refs/heads/fake/long/ref/name/with/some/extra/stuff",
			Hash: h1,
		},
		{
			Ref:  "refs/heads/fake/long/ref/name/with/some/extra/stuff",
			Hash: h2,
		},
	}

	err := c.PutRefs(context.Background(), "fakeserver", "fakeproject", refs)
	require.Error(err, "expected error from put refs")

	var e *remote.ErrObjectLinkValidation
	if errors.As(err, &e) {
		require.Len(e.MissingLinks, 2, "expected 2 missing links")
		require.Equal(h1, e.MissingLinks[0])
		require.Equal(h2, e.MissingLinks[1])
	} else {
		require.Fail("expected %s to have a type of remote.ErrObjectLinkValidation", err)
	}
}

func TestSHA256(t *testing.T) {
	require := require.New(t)
	c, close := Setup(t)
	defer close()
	ctx := context.Background()

	expectedData := []byte("This is some\nTest Data\n")
	testKey := objects.HashBlobBytes(expectedData)
	testExpectedSHA256 := entities.SHA256(sha256.Sum256(expectedData))
	err := c.PutObject(ctx, testKey, remote.NewBlobReaderFromBytes(expectedData))
	require.NoError(err, "failed to put")
	missingHash := entities.NewHash("00000dc642cb6eb9a060e54bf8d69288fbee0000")

	shas, err := c.SHA256(ctx, []remote.Hash{testKey, testKey, missingHash})
	require.NoError(err, "expected no error from sha256")
	require.Len(shas, 3, "expected 3 shas")
	require.Equal(testExpectedSHA256, shas[0], "expected sha256 to match")
	require.Equal(testExpectedSHA256, shas[1], "expected sha256 to match")
	require.Equal(entities.ZeroSHA256, shas[2], "expected zero SHA256 for missing key")
}
