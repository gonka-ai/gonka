package transport

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

type gzipEchoServer struct {
	*httptest.Server
	gzipped  atomic.Int32
	received atomic.Int64
}

func newGzipEchoServer(t testing.TB) *gzipEchoServer {
	t.Helper()
	echo := &gzipEchoServer{}
	response, err := proto.Marshal(&rpcpb.SeedHeightSyncResponse{})
	require.NoError(t, err)
	echo.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err == nil && r.Header.Get("Content-Encoding") == "gzip" {
			echo.gzipped.Add(1)
			var reader *gzip.Reader
			if reader, err = gzip.NewReader(bytes.NewReader(raw)); err == nil {
				raw, err = io.ReadAll(reader)
			}
		}
		var envelope rpcpb.SignedEnvelope
		if err != nil || proto.Unmarshal(raw, &envelope) != nil {
			http.Error(w, "undecodable request", http.StatusBadRequest)
			return
		}
		echo.received.Add(int64(len(envelope.Payload)))
		w.Header().Set("Content-Type", "application/proto")
		_, _ = w.Write(response)
	}))
	t.Cleanup(echo.Close)
	return echo
}

func (e *gzipEchoServer) send(t testing.TB, options []connect.ClientOption, payload []byte) {
	t.Helper()
	client := rpcpbconnect.NewSessionServiceClient(e.Client(), e.URL, options...)
	_, err := client.SeedHeightSync(context.Background(), connect.NewRequest(&rpcpb.SignedEnvelope{Payload: payload}))
	require.NoError(t, err)
}

// Test flow:
//  1. Take the gzip option for the default read cap.
//  2. Assert every client option set built for that cap carries it, a set for another cap does not, and chat clients carry the body-size one.
func TestClientOptionsShareOneGzipPoolPerReadCap(t *testing.T) {
	shared := withGzipReadCap(DefaultRPCReadMaxBytes)

	require.Contains(t, connectClientOptions(DefaultRPCReadMaxBytes), shared, "a client must register the shared gzip pool")
	require.Contains(t, connectClientOptions(DefaultRPCReadMaxBytes), shared, "a second client must register the same pool")
	require.NotContains(t, connectClientOptions(DefaultRPCQueryReadMaxBytes), shared, "a different cap needs its own decompressor, so its own pool")
	require.Contains(t, chatClientOptions(4<<10), withGzipReadCap(int(DefaultMaxBodySize)), "chat clients share the pool too")
}

// Test flow:
//  1. Start a server that gunzips Content-Encoding gzip request bodies and counts them.
//  2. Send an 8 KiB request from each of two clients built from separate `connectClientOptions` calls.
//  3. Assert both requests arrived gzipped, decoded to their full size and were answered without error.
func TestSharedGzipPoolStillRoundTrips(t *testing.T) {
	echo := newGzipEchoServer(t)
	payload := bytes.Repeat([]byte("prompt "), 8<<10/7)

	echo.send(t, connectClientOptions(DefaultRPCReadMaxBytes), payload)
	echo.send(t, connectClientOptions(DefaultRPCReadMaxBytes), payload)

	require.Equal(t, int32(2), echo.gzipped.Load(), "both requests must go out gzipped")
	require.Equal(t, int64(2*len(payload)), echo.received.Load(), "the server must decode every byte")
}

// Test flow:
//  1. Start the gzip echo server and build a fresh client from `connectClientOptions` for every request, as a session does per host.
//  2. Send one 8 KiB request per iteration.
//  3. Report allocated bytes and allocations per request.
func BenchmarkGzipRequestFromFreshClient(b *testing.B) {
	echo := newGzipEchoServer(b)
	payload := bytes.Repeat([]byte("prompt "), 8<<10/7)
	b.ReportAllocs()
	for b.Loop() {
		echo.send(b, connectClientOptions(DefaultRPCReadMaxBytes), payload)
	}
}
