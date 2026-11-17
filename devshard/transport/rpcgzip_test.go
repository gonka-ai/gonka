package transport

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

func TestCappedGzipReaderUnderCap(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), 100)
	compressed := gzipBytes(t, payload)
	r := newCappedGzipReader(1024)
	require.NoError(t, r.Reset(bytes.NewReader(compressed)))
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, payload, got)
	require.NoError(t, r.Close())
}

func TestCappedGzipReaderStopsAcrossMembers(t *testing.T) {
	const allow = 8 << 10
	first := gzipBytes(t, bytes.Repeat([]byte{0}, 1<<10))
	second := gzipBytes(t, bytes.Repeat([]byte{0}, 64<<10))
	bomb := append(append([]byte{}, first...), second...)

	src := &countReader{r: bytes.NewReader(bomb)}
	r := newCappedGzipReader(allow - 1)
	require.NoError(t, r.Reset(src))

	got, err := io.ReadAll(io.LimitReader(r, allow))
	require.NoError(t, err)
	require.Len(t, got, allow, "the cap must include bytes from the second gzip member")
	afterLimit := src.n

	_, err = r.Read(make([]byte, 32))
	require.ErrorIs(t, err, errGzipInflateCap)
	require.Equal(t, afterLimit, src.n, "the over-cap Read must not pull more compressed bytes")

	// A later message on the pooled reader still decodes.
	require.NoError(t, r.Reset(bytes.NewReader(gzipBytes(t, []byte("ok")))))
	again, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, []byte("ok"), again)
	require.NoError(t, r.Close())
}

func TestConnectClient_GzipBombStopsInflating(t *testing.T) {
	const capBytes = 16 << 10
	bomb := gzipMembers(t, 1<<20, 4)
	require.Less(t, len(bomb), capBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/proto")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bomb)
	}))
	t.Cleanup(srv.Close)

	client := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL, connectClientOptions(capBytes)...)
	_, err := client.GetDiffs(context.Background(), connect.NewRequest(&rpcpb.GetDiffsRequest{From: 1, To: 1}))
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Contains(t, err.Error(), "unable to determine message size")
	require.False(t, IsRetryableNonInference(err))
}

func TestConnectClient_UnderCapGzipRoundTrip(t *testing.T) {
	raw, err := proto.Marshal(&rpcpb.GetDiffsResponse{})
	require.NoError(t, err)
	compressed := gzipBytes(t, raw)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/proto")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(compressed)
	}))
	t.Cleanup(srv.Close)

	client := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL, connectClientOptions(16<<10)...)
	resp, err := client.GetDiffs(context.Background(), connect.NewRequest(&rpcpb.GetDiffsRequest{}))
	require.NoError(t, err)
	require.Empty(t, resp.Msg.GetRecords())
}

// connect-go decodes a non-200 unary error body without WithReadMaxBytes, so
// the decompressor cap is the only bound on a gzipped error body.
func TestConnectClient_GzipErrorBodyStopsInflating(t *testing.T) {
	const capBytes = 16 << 10
	wire := `{"code":"internal","message":"` + strings.Repeat("a", 4<<20) + `"}`
	bomb := gzipBytes(t, []byte(wire))
	require.Less(t, len(bomb), capBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(bomb)
	}))
	t.Cleanup(srv.Close)

	client := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL, connectClientOptions(capBytes)...)
	_, err := client.GetDiffs(context.Background(), connect.NewRequest(&rpcpb.GetDiffsRequest{}))
	require.Error(t, err)
	require.Less(t, len(err.Error()), capBytes, "the 4 MiB wire message must not be decoded into the error")
}

func TestChatClient_GzipBombStopsInflating(t *testing.T) {
	bomb := gzipMembers(t, 1<<20, 12)
	require.Less(t, len(bomb), int(DefaultMaxBodySize))
	var env bytes.Buffer
	env.WriteByte(0x01) // Connect envelope flag: compressed
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(bomb)))
	env.Write(size[:])
	env.Write(bomb)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/connect+proto")
		w.Header().Set("Connect-Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(env.Bytes())
	}))
	t.Cleanup(srv.Close)

	client := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL, chatClientOptions(0)...)
	stream, err := client.Chat(context.Background(), connect.NewRequest(&rpcpb.SignedEnvelope{}))
	if err == nil {
		require.False(t, stream.Receive())
		err = stream.Err()
	}
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Contains(t, err.Error(), "unable to determine message size")
	require.False(t, IsRetryableNonInference(err))
}

func gzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	require.NoError(t, err)
	_, err = zw.Write(payload)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func gzipMembers(t *testing.T, decoded, members int) []byte {
	t.Helper()
	one := gzipBytes(t, bytes.Repeat([]byte{0}, decoded))
	require.Greater(t, members, 1)
	return bytes.Repeat(one, members)
}

type countReader struct {
	r io.Reader
	n int
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

type silentChatHandler struct {
	rpcpbconnect.UnimplementedSessionServiceHandler
}

func (silentChatHandler) Chat(context.Context, *connect.Request[rpcpb.SignedEnvelope], *connect.ServerStream[rpcpb.ChatFrame]) error {
	return nil
}

func chatEnvelopeCompressed(t *testing.T, compressMinBytes, payloadBytes int) bool {
	t.Helper()
	var firstEnvelopeFlags atomic.Int32
	var bodySeen atomic.Bool
	path, handler := rpcpbconnect.NewSessionServiceHandler(silentChatHandler{})
	mux := http.NewServeMux()
	mux.Handle(path, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err == nil && len(body) > 0 {
			firstEnvelopeFlags.Store(int32(body[0]))
			bodySeen.Store(true)
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		handler.ServeHTTP(writer, request)
	}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := rpcpbconnect.NewSessionServiceClient(server.Client(), server.URL, chatClientOptions(compressMinBytes)...)
	stream, err := client.Chat(context.Background(), connect.NewRequest(&rpcpb.SignedEnvelope{Payload: bytes.Repeat([]byte("prompt "), payloadBytes/7)}))
	require.NoError(t, err)
	for stream.Receive() {
	}
	require.NoError(t, stream.Err())
	require.NoError(t, stream.Close())
	require.True(t, bodySeen.Load(), "the server never read the request envelope")
	return firstEnvelopeFlags.Load()&1 == 1
}

// Test flow:
//  1. Send a Chat request through chatClientOptions with a 4 KiB threshold and a 2 KiB and an 8 KiB payload, and with no threshold and a 2 KiB payload.
//  2. Read the compression flag of the request's first envelope on the server.
//  3. Assert only the payload past the threshold is gzipped, and that no threshold keeps gzipping every request as before.
func TestChatClientCompressesOnlyPastTheThreshold(t *testing.T) {
	require.False(t, chatEnvelopeCompressed(t, 4<<10, 2<<10), "a 2 KiB request under a 4 KiB threshold must go uncompressed")
	require.True(t, chatEnvelopeCompressed(t, 4<<10, 8<<10), "an 8 KiB request past a 4 KiB threshold must be gzipped")
	require.True(t, chatEnvelopeCompressed(t, 0, 2<<10), "with no threshold every request is gzipped")
}
