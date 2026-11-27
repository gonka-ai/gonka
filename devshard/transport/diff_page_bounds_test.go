package transport

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/internal/testutil"
	"devshard/storage"
	"devshard/transport/rpcpb"
	"devshard/types"
)

func rawBodyDiffsServer(t *testing.T, body []byte) *HTTPClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	cfg := DefaultClientConfig()
	cfg.RoutePrefix = ""
	return NewHTTPClient(srv.URL, "escrow-1", testutil.MustGenerateKey(t), cfg)
}

func emptyRecordsJSON(n int) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{}`)
	}
	b.WriteByte(']')
	return b.Bytes()
}

func totalAlloc(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestHTTPClient_GetDiffs_CountsRecordsBeforeDecode(t *testing.T) {
	body := emptyRecordsJSON(1 << 20)
	require.Less(t, int64(len(body)), maxDiffPageBodyBytes)
	client := rawBodyDiffsServer(t, body)

	var err error
	alloc := totalAlloc(func() {
		_, err = client.GetDiffs(context.Background(), 1, storage.DiffPageMaxNonces)
	})
	require.ErrorIs(t, err, ErrDiffPageOversized)
	require.Less(t, alloc, uint64(32<<20))
}

func TestHTTPClient_GetDiffs_AcceptsFullPageAndNull(t *testing.T) {
	records := make([]DiffJSON, storage.DiffPageMaxNonces)
	for i := range records {
		dj, err := DiffToJSON(types.Diff{Nonce: uint64(i + 1)})
		require.NoError(t, err)
		records[i] = dj
	}
	diffs, err := rawDiffsServer(t, records).GetDiffs(context.Background(), 1, storage.DiffPageMaxNonces)
	require.NoError(t, err)
	require.Len(t, diffs, storage.DiffPageMaxNonces)

	diffs, err = rawBodyDiffsServer(t, []byte(`null`)).GetDiffs(context.Background(), 1, 1)
	require.NoError(t, err)
	require.Empty(t, diffs)
}

func emptyRecordsProto(n int) []byte {
	return bytes.Repeat([]byte{0x0a, 0x00}, n)
}

func TestBoundedCodec_CountsGetDiffsRecordsBeforeDecode(t *testing.T) {
	data := emptyRecordsProto(1 << 22)
	var err error
	alloc := totalAlloc(func() {
		err = boundedCodec{}.Unmarshal(data, &rpcpb.GetDiffsResponse{})
	})
	require.ErrorIs(t, err, ErrDiffPageOversized)
	require.Less(t, alloc, uint64(1<<20))

	var resp rpcpb.GetDiffsResponse
	require.NoError(t, boundedCodec{}.Unmarshal(emptyRecordsProto(storage.DiffPageMaxNonces), &resp))
	require.Len(t, resp.GetRecords(), storage.DiffPageMaxNonces)
}

func TestBoundedCodec_MatchesProto(t *testing.T) {
	msg := &rpcpb.GetDiffsResponse{Records: []*rpcpb.DiffRecord{{Diff: &rpcpb.Diff{Nonce: 7, UserSig: []byte{1}}, StateHash: []byte{2}}}}
	got, err := boundedCodec{}.Marshal(msg)
	require.NoError(t, err)
	want, err := proto.Marshal(msg)
	require.NoError(t, err)
	require.Equal(t, want, got)

	var back rpcpb.GetDiffsResponse
	require.NoError(t, boundedCodec{}.Unmarshal(got, &back))
	require.True(t, proto.Equal(msg, &back))

	require.Error(t, boundedCodec{}.Unmarshal([]byte{0x0a, 0x05}, &rpcpb.GetDiffsResponse{}))
	require.NoError(t, boundedCodec{}.Unmarshal(got, &rpcpb.GetMempoolResponse{}))
}
