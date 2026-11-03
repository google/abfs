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

package casdb

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"cloud.google.com/go/spanner"
	"cloud.google.com/go/spanner/apiv1/spannerpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/remote"
)

var (
	internSink  string
	headersSink []*ObjectHeader
)

type refRowKey struct {
	projectID int64
	ref       string
}

type fakeRefsSpannerServer struct {
	spannerpb.UnimplementedSpannerServer

	mu          sync.Mutex
	nextSession int
	nextTx      int
	refs        map[refRowKey]spanner.NullString
	commitCount int
}

func newFakeRefsSpannerServer() *fakeRefsSpannerServer {
	return &fakeRefsSpannerServer{
		refs: make(map[refRowKey]spanner.NullString),
	}
}

func (f *fakeRefsSpannerServer) CreateSession(_ context.Context, req *spannerpb.CreateSessionRequest) (*spannerpb.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextSession++
	return &spannerpb.Session{
		Name:        fmt.Sprintf("%s/sessions/s-%d", req.GetDatabase(), f.nextSession),
		Multiplexed: req.GetSession().GetMultiplexed(),
	}, nil
}

func (f *fakeRefsSpannerServer) BatchCreateSessions(_ context.Context, req *spannerpb.BatchCreateSessionsRequest) (*spannerpb.BatchCreateSessionsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := int(req.GetSessionCount())
	if count <= 0 {
		count = 1
	}
	sessions := make([]*spannerpb.Session, count)
	for i := range count {
		f.nextSession++
		sessions[i] = &spannerpb.Session{
			Name: fmt.Sprintf("%s/sessions/s-%d", req.GetDatabase(), f.nextSession),
		}
	}
	return &spannerpb.BatchCreateSessionsResponse{Session: sessions}, nil
}

func (f *fakeRefsSpannerServer) GetSession(_ context.Context, req *spannerpb.GetSessionRequest) (*spannerpb.Session, error) {
	return &spannerpb.Session{Name: req.GetName()}, nil
}

func (f *fakeRefsSpannerServer) DeleteSession(_ context.Context, _ *spannerpb.DeleteSessionRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (f *fakeRefsSpannerServer) BeginTransaction(_ context.Context, _ *spannerpb.BeginTransactionRequest) (*spannerpb.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextTx++
	return &spannerpb.Transaction{Id: []byte(fmt.Sprintf("tx-%d", f.nextTx))}, nil
}

func (f *fakeRefsSpannerServer) Rollback(_ context.Context, _ *spannerpb.RollbackRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (f *fakeRefsSpannerServer) ExecuteStreamingSql(req *spannerpb.ExecuteSqlRequest, stream spannerpb.Spanner_ExecuteStreamingSqlServer) error {
	f.mu.Lock()
	var txMeta *spannerpb.Transaction
	if req.GetTransaction().GetBegin() != nil {
		f.nextTx++
		txMeta = &spannerpb.Transaction{Id: []byte(fmt.Sprintf("tx-%d", f.nextTx))}
	}

	sql := req.GetSql()
	switch {
	case strings.Contains(sql, "FROM\n\t\t\t  UNNEST(@refs) AS r,\n\t\t\t  Refs AS refs"):
		fields := req.GetParams().GetFields()
		projectID, err := strconv.ParseInt(fields["projectId"].GetStringValue(), 10, 64)
		if err != nil {
			f.mu.Unlock()
			return status.Errorf(codes.InvalidArgument, "invalid projectId: %v", err)
		}
		var values []*structpb.Value
		for _, v := range fields["refs"].GetListValue().GetValues() {
			refName := v.GetStringValue()
			target, exists := f.refs[refRowKey{projectID: projectID, ref: refName}]
			if !exists {
				continue
			}
			values = append(values, structpb.NewStringValue(refName))
			if target.Valid {
				values = append(values, structpb.NewStringValue(target.StringVal))
			} else {
				values = append(values, structpb.NewNullValue())
			}
		}
		f.mu.Unlock()

		return stream.Send(&spannerpb.PartialResultSet{
			Metadata: &spannerpb.ResultSetMetadata{
				Transaction: txMeta,
				RowType: &spannerpb.StructType{
					Fields: []*spannerpb.StructType_Field{
						{Name: "Ref", Type: &spannerpb.Type{Code: spannerpb.TypeCode_STRING}},
						{Name: "Target", Type: &spannerpb.Type{Code: spannerpb.TypeCode_STRING}},
					},
				},
			},
			Values: values,
		})
	default:
		f.mu.Unlock()
		return status.Errorf(codes.Unimplemented, "unsupported SQL in fake: %s", sql)
	}
}

func (f *fakeRefsSpannerServer) Commit(_ context.Context, req *spannerpb.CommitRequest) (*spannerpb.CommitResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	seenInTx := make(map[refRowKey]struct{}, len(req.GetMutations()))
	type pendingWrite struct {
		key    refRowKey
		target spanner.NullString
	}
	writes := make([]pendingWrite, 0, len(req.GetMutations()))

	for _, m := range req.GetMutations() {
		iou := m.GetInsertOrUpdate()
		if iou == nil || iou.GetTable() != "Refs" {
			return nil, status.Errorf(codes.InvalidArgument, "unexpected mutation: %v", m)
		}
		for _, row := range iou.GetValues() {
			vals := row.GetValues()
			projectID, err := strconv.ParseInt(vals[0].GetStringValue(), 10, 64)
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "invalid ProjectId: %v", err)
			}
			refName := vals[1].GetStringValue()
			key := refRowKey{projectID: projectID, ref: refName}
			// Real Cloud Spanner rejects multiple mutations to the same row in one transaction.
			if _, dup := seenInTx[key]; dup {
				return nil, status.Errorf(codes.InvalidArgument, "Table Refs: Cannot modify a row more than once per transaction: %v", key)
			}
			seenInTx[key] = struct{}{}

			var target spanner.NullString
			if _, isNull := vals[3].GetKind().(*structpb.Value_NullValue); !isNull {
				target = spanner.NullString{StringVal: vals[3].GetStringValue(), Valid: true}
			}
			writes = append(writes, pendingWrite{key: key, target: target})
		}
	}

	for _, w := range writes {
		f.refs[w.key] = w.target
	}
	f.commitCount++
	return &spannerpb.CommitResponse{CommitTimestamp: timestamppb.Now()}, nil
}

func newTestCASDBWithFakeSpanner(t *testing.T) (*CASDB, *fakeRefsSpannerServer) {
	t.Helper()
	fake := newFakeRefsSpannerServer()
	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	spannerpb.RegisterSpannerServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})

	ctx := context.Background()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	cfg := spanner.ClientConfig{
		SessionPoolConfig: spanner.SessionPoolConfig{
			MinOpened: 1,
			MaxOpened: 4,
		},
		DisableNativeMetrics: true,
	}
	dbClient, err := spanner.NewClientWithConfig(
		ctx,
		"projects/test-proj/instances/test-inst/databases/test-db",
		cfg,
		option.WithGRPCConn(conn),
		option.WithoutAuthentication(),
	)
	require.NoError(t, err)
	t.Cleanup(dbClient.Close)

	return &CASDB{dbClient: dbClient}, fake
}

func TestXchgRefs(t *testing.T) {
	ctx := context.Background()
	db, fake := newTestCASDBWithFakeSpanner(t)
	const projectID int64 = 42

	hashA := entities.NewHash("a0423896973644771497bdc03eb99d5281615b50")
	hashB := entities.NewHash("b0423896973644771497bdc03eb99d5281615b51")
	hashC := entities.NewHash("c0423896973644771497bdc03eb99d5281615b52")

	t.Run("empty_slice_is_noop", func(t *testing.T) {
		require.NoError(t, db.XchgRefs(ctx, projectID, nil))
		assert.Equal(t, 0, fake.commitCount)
	})

	t.Run("exceeding_max_batch_fails_early", func(t *testing.T) {
		tooMany := make([]remote.RefXchg, maxRefsBatch+1)
		err := db.XchgRefs(ctx, projectID, tooMany)
		require.ErrorContains(t, err, fmt.Sprintf("max %d", maxRefsBatch))
		assert.Equal(t, 0, fake.commitCount)
	})

	t.Run("create_and_update_refs", func(t *testing.T) {
		// Create two new refs from ZeroHash.
		err := db.XchgRefs(ctx, projectID, []remote.RefXchg{
			{Ref: "refs/heads/main", Current: entities.ZeroHash, Desired: hashA},
			{Ref: "refs/heads/dev", Current: entities.ZeroHash, Desired: hashB},
		})
		require.NoError(t, err)

		got, err := db.GetRefs(ctx, projectID, []string{"refs/heads/main", "refs/heads/dev", "refs/heads/missing"})
		require.NoError(t, err)
		assert.Equal(t, []entities.Hash{hashA, hashB, entities.ZeroHash}, got)

		// Advance refs/heads/main from hashA -> hashC.
		err = db.XchgRefs(ctx, projectID, []remote.RefXchg{
			{Ref: "refs/heads/main", Current: hashA, Desired: hashC},
		})
		require.NoError(t, err)

		got, err = db.GetRefs(ctx, projectID, []string{"refs/heads/main"})
		require.NoError(t, err)
		assert.Equal(t, []entities.Hash{hashC}, got)
	})

	t.Run("mismatch_aborts_entire_batch_and_returns_ErrRefMismatch", func(t *testing.T) {
		commitsBefore := fake.commitCount
		err := db.XchgRefs(ctx, projectID, []remote.RefXchg{
			{Ref: "refs/heads/dev", Current: hashB, Desired: hashC},  // valid
			{Ref: "refs/heads/main", Current: hashA, Desired: hashB}, // stale Current (actual is hashC)
		})
		var mismatchErr *remote.ErrRefMismatch
		require.ErrorAs(t, err, &mismatchErr)
		assert.Equal(t, "refs/heads/main", mismatchErr.Ref)
		assert.Equal(t, hashA, mismatchErr.Current)
		assert.Equal(t, hashC, mismatchErr.Actual)
		assert.Equal(t, commitsBefore, fake.commitCount)

		// Verify refs/heads/dev was not modified either.
		got, err := db.GetRefs(ctx, projectID, []string{"refs/heads/main", "refs/heads/dev"})
		require.NoError(t, err)
		assert.Equal(t, []entities.Hash{hashC, hashB}, got)
	})

	t.Run("chained_updates_to_same_ref_in_single_batch", func(t *testing.T) {
		// Chained transition: hashC -> hashA -> hashB on refs/heads/main in one transaction.
		// Also verifies fakeSpannerServer's single-mutation-per-row constraint is satisfied.
		err := db.XchgRefs(ctx, projectID, []remote.RefXchg{
			{Ref: "refs/heads/main", Current: hashC, Desired: hashA},
			{Ref: "refs/heads/main", Current: hashA, Desired: hashB},
		})
		require.NoError(t, err)

		got, err := db.GetRefs(ctx, projectID, []string{"refs/heads/main"})
		require.NoError(t, err)
		assert.Equal(t, []entities.Hash{hashB}, got)

		// Broken chain on second step fails with ErrRefMismatch against intermediate hash.
		err = db.XchgRefs(ctx, projectID, []remote.RefXchg{
			{Ref: "refs/heads/main", Current: hashB, Desired: hashA},
			{Ref: "refs/heads/main", Current: hashB, Desired: hashC},
		})
		var mismatchErr *remote.ErrRefMismatch
		require.ErrorAs(t, err, &mismatchErr)
		assert.Equal(t, "refs/heads/main", mismatchErr.Ref)
		assert.Equal(t, hashB, mismatchErr.Current)
		assert.Equal(t, hashA, mismatchErr.Actual)
	})

	t.Run("tombstone_zero_hash_and_recreate", func(t *testing.T) {
		// Delete refs/heads/main by setting Desired to ZeroHash.
		err := db.XchgRefs(ctx, projectID, []remote.RefXchg{
			{Ref: "refs/heads/main", Current: hashB, Desired: entities.ZeroHash},
		})
		require.NoError(t, err)

		got, err := db.GetRefs(ctx, projectID, []string{"refs/heads/main"})
		require.NoError(t, err)
		assert.Equal(t, []entities.Hash{entities.ZeroHash}, got)

		// Recreate refs/heads/main from ZeroHash -> hashA.
		err = db.XchgRefs(ctx, projectID, []remote.RefXchg{
			{Ref: "refs/heads/main", Current: entities.ZeroHash, Desired: hashA},
		})
		require.NoError(t, err)

		got, err = db.GetRefs(ctx, projectID, []string{"refs/heads/main"})
		require.NoError(t, err)
		assert.Equal(t, []entities.Hash{hashA}, got)
	})
}

func TestBigtableClientOptions(t *testing.T) {
	tests := []struct {
		name       string
		btConnPool int
		wantLen    int
	}{
		{name: "positive pool sets one option", btConnPool: 50, wantLen: 1},
		{name: "one connection sets one option", btConnPool: 1, wantLen: 1},
		{name: "zero leaves library default", btConnPool: 0, wantLen: 0},
		{name: "negative leaves library default", btConnPool: -1, wantLen: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(bigtableClientOptions(tc.btConnPool)); got != tc.wantLen {
				t.Errorf("bigtableClientOptions(%d) = %d options, want %d", tc.btConnPool, got, tc.wantLen)
			}
		})
	}
}

func TestInternObjectType(t *testing.T) {
	knownTypes := []string{
		string(entities.ObjectTypeBlob),
		string(gitobjects.ObjectTypeDir),
		string(gitobjects.ObjectTypeCommit),
		string(gitobjects.ObjectTypeTag),
		string(gitobjects.ObjectTypeSymRef),
		string(gitobjects.ObjectTypeDeepTree),
		string(objects.ObjectTypeDeepTreeRequest),
	}

	for _, canonical := range knownTypes {
		t.Run(canonical, func(t *testing.T) {
			b1 := slices.Clone([]byte(canonical))
			b2 := slices.Clone([]byte(canonical))
			if unsafe.SliceData(b1) == unsafe.SliceData(b2) {
				t.Fatalf("expected distinct backing arrays for b1 and b2")
			}

			s1 := internObjectType(b1)
			s2 := internObjectType(b2)

			if s1 != canonical || s2 != canonical {
				t.Fatalf("got (%q, %q), want %q", s1, s2, canonical)
			}

			// 1. Prove both returned strings share the exact same underlying static data pointer
			// as the compile-time constant string, rather than pointing to heap allocations.
			if unsafe.StringData(s1) != unsafe.StringData(s2) {
				t.Errorf("expected identical underlying string data pointers: s1=%p, s2=%p", unsafe.StringData(s1), unsafe.StringData(s2))
			}
			if unsafe.StringData(s1) != unsafe.StringData(canonical) {
				t.Errorf("expected interned string pointer %p to match constant rodata pointer %p", unsafe.StringData(s1), unsafe.StringData(canonical))
			}

			// 2. Prove zero heap allocations per invocation.
			allocs := testing.AllocsPerRun(1000, func() {
				internSink = internObjectType(b1)
			})
			if allocs != 0 {
				t.Errorf("internObjectType(%q) allocated %v times per run, want 0", canonical, allocs)
			}

			// 3. Prove mutating the input byte slice does not mutate the interned string.
			b1[0] ^= 0xff
			if s1 != canonical {
				t.Errorf("mutating input byte slice corrupted interned string: got %q, want %q", s1, canonical)
			}
		})
	}

	t.Run("unknown_custom_type_fallback", func(t *testing.T) {
		custom := []byte("custom-type")
		got := internObjectType(custom)
		if got != "custom-type" {
			t.Fatalf("got %q, want %q", got, "custom-type")
		}
		allocs := testing.AllocsPerRun(1000, func() {
			internSink = internObjectType(custom)
		})
		if allocs != 1 {
			t.Errorf("expected unlisted type to allocate 1 time per run on heap, got %v", allocs)
		}
	})
}

func TestInternObjectTypeHeapComparison(t *testing.T) {
	const count = 1_000_000
	rawCellValue := []byte("deeptree-request")

	measureRetained := func(convert func([]byte) string) (heapBytes uint64, heapObjects uint64) {
		headersSink = nil
		runtime.GC()
		runtime.GC()

		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)

		s := make([]*ObjectHeader, count)
		for i := range count {
			s[i] = &ObjectHeader{
				Type: convert(rawCellValue),
				Size: int64(i),
			}
		}
		headersSink = s

		runtime.GC()
		runtime.GC()
		runtime.ReadMemStats(&after)
		runtime.KeepAlive(headersSink)

		return after.HeapAlloc - before.HeapAlloc, after.HeapObjects - before.HeapObjects
	}

	naiveBytes, naiveObjs := measureRetained(func(b []byte) string { return string(b) })
	internBytes, internObjs := measureRetained(internObjectType)
	headersSink = nil

	savedBytes := int64(naiveBytes) - int64(internBytes)
	savedObjs := int64(naiveObjs) - int64(internObjs)

	t.Logf("Before (string(b)):        %8.2f MiB retained (%d heap objects) for %d headers", float64(naiveBytes)/(1024*1024), naiveObjs, count)
	t.Logf("After  (internObjectType): %8.2f MiB retained (%d heap objects) for %d headers", float64(internBytes)/(1024*1024), internObjs, count)
	t.Logf("Saved per 1M headers:      %8.2f MiB (%d fewer heap objects; %.2f GiB projected at 100M cached headers)",
		float64(savedBytes)/(1024*1024), savedObjs, float64(savedBytes)*100/(1024*1024*1024))

	if internBytes >= naiveBytes {
		t.Errorf("expected internObjectType to use less heap than string(b): got %d B vs %d B", internBytes, naiveBytes)
	}
}

func BenchmarkInternObjectType(b *testing.B) {
	input := []byte("blob")
	b.ReportAllocs()
	for b.Loop() {
		internSink = internObjectType(input)
	}
}

func BenchmarkNaiveStringConversion(b *testing.B) {
	input := []byte("blob")
	b.ReportAllocs()
	for b.Loop() {
		internSink = string(input)
	}
}
