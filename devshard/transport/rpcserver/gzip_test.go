package rpcserver

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/types"
)

func TestHandshakeGate_GzipBombRejectedBeforeHandler(t *testing.T) {
	var ran atomic.Bool
	lookup := stubLookup{core: gossipCore{
		stubCore: stubCore{member: true},
		onNonce: func(transport.GossipNonceRequest) error {
			ran.Store(true)
			return nil
		},
	}}
	auth := newTestAuth(PeerAuthConfig{})
	mux := NewMux(auth, NewSessionHandler(lookup), WithGossipService(NewGossipHandler(lookup)))
	srv := httptest.NewServer(withTestEscrow(mux))
	t.Cleanup(srv.Close)
	signer := testutil.MustGenerateKey(t)
	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer, []byte("gzip-bomb-attach-nonce-012"))

	env := &rpcpb.SignedEnvelope{
		Payload:   bytes.Repeat([]byte("n"), 100<<10),
		EscrowId:  "1",
		Timestamp: 1,
		Signature: []byte{1},
	}
	raw, err := proto.Marshal(env)
	require.NoError(t, err)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err = zw.Write(raw)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.Less(t, buf.Len(), transport.DefaultRPCReadMaxBytes)

	req, err := http.NewRequest(http.MethodPost, srv.URL+rpcpbconnect.GossipServiceNonceProcedure, bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Content-Encoding", "gzip")
	SetSessionHeader(req.Header, attached.SessionToken)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.False(t, ran.Load(), "gzip bomb must not reach ServeGossipNonce")
}

func TestGetPayload_ReadCapAllowsOver16KiB(t *testing.T) {
	var saw atomic.Int64
	auth := newTestAuth(PeerAuthConfig{})
	lookup := stubLookup{core: stubCore{member: true}}
	h := NewPayloadHandler(lookup, func(ctx context.Context, _ SessionCore, _ string, req *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		saw.Store(int64(len(req.GetSignature())))
		return &rpcpb.GetPayloadResponse{InferenceId: req.GetInferenceId()}, nil
	})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, NewSessionHandler(lookup), WithPayloadService(h))))
	t.Cleanup(srv.Close)
	signer := testutil.MustGenerateKey(t)
	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer, []byte("payload-cap-attach-nonce-01"))

	client := rpcpbconnect.NewPayloadServiceClient(srv.Client(), srv.URL, connect.WithSendGzip())
	sig := bytes.Repeat([]byte{0xab}, transport.DefaultRPCReadMaxBytes+1024)
	_, err := client.GetPayload(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetPayloadRequest{InferenceId: "1", Signature: sig}),
		attached.SessionToken,
	))
	require.NoError(t, err)
	require.Equal(t, int64(len(sig)), saw.Load())
}

func TestGetPayload_GzipBombRejectedBeforeHandler(t *testing.T) {
	var ran atomic.Bool
	auth := newTestAuth(PeerAuthConfig{})
	lookup := stubLookup{core: stubCore{member: true}}
	h := NewPayloadHandler(lookup, func(context.Context, SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		ran.Store(true)
		return &rpcpb.GetPayloadResponse{InferenceId: "1"}, nil
	})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, NewSessionHandler(lookup), WithPayloadService(h))))
	t.Cleanup(srv.Close)
	signer := testutil.MustGenerateKey(t)
	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer, []byte("payload-bomb-attach-nonce-01"))

	raw, err := proto.Marshal(&rpcpb.GetPayloadRequest{
		InferenceId: "1",
		Signature:   bytes.Repeat([]byte{0xab}, int(transport.DefaultMaxBodySize)+1),
	})
	require.NoError(t, err)
	require.Greater(t, len(raw), int(transport.DefaultMaxBodySize))
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err = zw.Write(raw)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.Less(t, buf.Len(), int(transport.DefaultMaxBodySize), "compressed bomb must fit so the reject is the inflate cap")

	req, err := http.NewRequest(http.MethodPost, srv.URL+rpcpbconnect.PayloadServiceGetPayloadProcedure, bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Content-Encoding", "gzip")
	SetSessionHeader(req.Header, attached.SessionToken)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.False(t, ran.Load(), "gzip bomb must not reach ServeRPCGetPayload")
}

type chatCore struct {
	stubCore
	ran  *atomic.Bool
	body *atomic.Int64
	emit []byte
}

func (c chatCore) ServeInference(_ context.Context, call transport.InferenceCall) error {
	if c.ran != nil {
		c.ran.Store(true)
	}
	if c.body != nil {
		c.body.Store(int64(len(call.Body)))
	}
	if len(c.emit) > 0 && call.Sink != nil {
		if _, err := call.Sink.Write(c.emit); err != nil {
			return err
		}
		call.Sink.Flush()
	}
	return nil
}

func TestChat_ReadCapAllowsOver16KiB(t *testing.T) {
	var ran atomic.Bool
	lookup := stubLookup{core: chatCore{stubCore: stubCore{owner: true}, ran: &ran}}
	env := newSessionEnv(t, lookup, testEscrowID)
	payload := bytes.Repeat([]byte("p"), transport.DefaultRPCReadMaxBytes+1024)
	envSigned, err := transport.SignEnvelope(env.signer, testEscrowID, payload, time.Now().Unix())
	require.NoError(t, err)
	stream, err := env.session.Chat(context.Background(), withSession(connect.NewRequest(envSigned), env.token))
	require.NoError(t, err)
	require.False(t, stream.Receive())
	require.NoError(t, stream.Err())
	require.True(t, ran.Load(), "Chat must admit a 16 KiB+ envelope (10 MiB cap)")
}

// chatGzipEnv is an attached Chat client that sends gzipped envelopes, plus a
// tap on the Chat procedure.
func chatGzipEnv(t *testing.T, lookup SessionLookup, nonce string) (rpcpbconnect.SessionServiceClient, []byte, *signing.Secp256k1Signer, *wireTap) {
	t.Helper()
	tap := &wireTap{procedure: rpcpbconnect.SessionServiceChatProcedure}
	srv := newTapServer(t, NewMux(newTestAuth(PeerAuthConfig{}), NewSessionHandler(lookup)), tap)
	signer := testutil.MustGenerateKey(t)
	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer, []byte(nonce))
	client := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL,
		connect.WithReadMaxBytes(int(transport.DefaultMaxBodySize)), connect.WithSendGzip())
	return client, attached.SessionToken, signer, tap
}

func TestChat_RequestEnvelopeIsGzipped(t *testing.T) {
	var body atomic.Int64
	lookup := stubLookup{core: chatCore{stubCore: stubCore{owner: true}, body: &body}}
	client, token, signer, tap := chatGzipEnv(t, lookup, "chat-gzip-attach-nonce-0123")

	payload := bytes.Repeat([]byte("prompt tokens "), 8<<10)
	env, err := transport.SignEnvelope(signer, testEscrowID, payload, time.Now().Unix())
	require.NoError(t, err)
	stream, err := client.Chat(context.Background(), withSession(connect.NewRequest(env), token))
	require.NoError(t, err)
	require.False(t, stream.Receive())
	require.NoError(t, stream.Err())

	require.Equal(t, int64(len(payload)), body.Load(), "the handler must see the inflated prompt")
	require.Equal(t, "gzip", tap.requestEncoding())
	require.Less(t, tap.requestBytes(), len(payload)/4,
		"a compressible prompt must be materially smaller on the wire")
}

func TestChat_ResponseFramesAreNotCompressedTwice(t *testing.T) {
	emit := bytes.Repeat([]byte("data: token\n\n"), 4<<10)
	lookup := stubLookup{core: chatCore{stubCore: stubCore{owner: true}, emit: emit}}
	client, token, signer, tap := chatGzipEnv(t, lookup, "chat-frames-attach-nonce-01")

	env, err := transport.SignEnvelope(signer, testEscrowID, []byte("prompt"), time.Now().Unix())
	require.NoError(t, err)
	stream, err := client.Chat(context.Background(), withSession(connect.NewRequest(env), token))
	require.NoError(t, err)
	var chunks [][]byte
	for stream.Receive() {
		chunks = append(chunks, append([]byte(nil), stream.Msg().GetChunk()...))
	}
	require.NoError(t, stream.Err())
	require.NotEmpty(t, chunks)

	for i, flag := range tap.responseEnvelopeFlags(t) {
		require.Zero(t, flag&connectFlagCompressed, "response envelope %d is gzip inside gzip", i)
	}
	zr, err := gzip.NewReader(bytes.NewReader(bytes.Join(chunks, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = zr.Close() })
	decoded, err := io.ReadAll(zr)
	require.NoError(t, err)
	require.Equal(t, emit, decoded, "chunks concatenate into exactly one application gzip stream")
}

func TestChat_GzipBombRejectedBeforeHandler(t *testing.T) {
	var ran atomic.Bool
	lookup := stubLookup{core: chatCore{stubCore: stubCore{owner: true}, ran: &ran}}
	client, token, signer, tap := chatGzipEnv(t, lookup, "chat-bomb-attach-nonce-0123")

	payload := bytes.Repeat([]byte("n"), int(transport.DefaultMaxBodySize)+1)
	env, err := transport.SignEnvelope(signer, testEscrowID, payload, time.Now().Unix())
	require.NoError(t, err)
	stream, err := client.Chat(context.Background(), withSession(connect.NewRequest(env), token))
	if err == nil {
		require.False(t, stream.Receive())
		err = stream.Err()
	}
	require.Error(t, err)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.False(t, ran.Load(), "an over-cap envelope must not reach ServeInference")
	require.Less(t, tap.requestBytes(), int(transport.DefaultMaxBodySize),
		"the compressed bomb fits; the reject is the inflate cap")
}

func TestUnaryRPC_RequestAndResponseAreGzipped(t *testing.T) {
	lookup := stubLookup{core: stubCore{
		member:  true,
		diffs:   []types.DiffRecord{{Diff: types.Diff{Nonce: 1, UserSig: make([]byte, 8<<10)}}},
		mempool: []*types.DevshardTx{testutil.StartTx(1)},
	}}
	payload := NewPayloadHandler(lookup, func(_ context.Context, _ SessionCore, _ string, req *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		return &rpcpb.GetPayloadResponse{
			InferenceId:   req.GetInferenceId(),
			PromptPayload: bytes.Repeat([]byte("p"), 8<<10),
		}, nil
	})
	mux := NewMux(newTestAuth(PeerAuthConfig{}), NewSessionHandler(lookup), WithPayloadService(payload))

	tests := []struct {
		name      string
		procedure string
		call      func(t *testing.T, srv *httptest.Server, token []byte)
	}{
		{
			name:      "GetDiffs",
			procedure: rpcpbconnect.SessionServiceGetDiffsProcedure,
			call: func(t *testing.T, srv *httptest.Server, token []byte) {
				_, err := unaryGzipClient(srv).GetDiffs(context.Background(), withSession(
					connect.NewRequest(&rpcpb.GetDiffsRequest{From: 1, To: 1}), token))
				require.NoError(t, err)
			},
		},
		{
			name:      "GetMempool",
			procedure: rpcpbconnect.SessionServiceGetMempoolProcedure,
			call: func(t *testing.T, srv *httptest.Server, token []byte) {
				_, err := unaryGzipClient(srv).GetMempool(context.Background(), withSession(
					connect.NewRequest(&rpcpb.GetMempoolRequest{}), token))
				require.NoError(t, err)
			},
		},
		{
			name:      "GetPayload",
			procedure: rpcpbconnect.PayloadServiceGetPayloadProcedure,
			call: func(t *testing.T, srv *httptest.Server, token []byte) {
				client := rpcpbconnect.NewPayloadServiceClient(srv.Client(), srv.URL, connect.WithSendGzip())
				_, err := client.GetPayload(context.Background(), withSession(
					connect.NewRequest(&rpcpb.GetPayloadRequest{InferenceId: "1"}), token))
				require.NoError(t, err)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tap := &wireTap{procedure: tc.procedure}
			srv := newTapServer(t, mux, tap)
			signer := testutil.MustGenerateKey(t)
			attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer,
				[]byte("gzip-unary-nonce-"+tc.name))
			tc.call(t, srv, attached.SessionToken)
			require.Equal(t, "gzip", tap.requestEncoding())
			require.Equal(t, "gzip", tap.responseEncoding())
		})
	}
}

func unaryGzipClient(srv *httptest.Server) rpcpbconnect.SessionServiceClient {
	return rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL,
		connect.WithReadMaxBytes(transport.DefaultRPCQueryReadMaxBytes), connect.WithSendGzip())
}

// connectFlagCompressed is the Connect envelope prefix bit that marks a
// message the framework compressed.
const connectFlagCompressed byte = 0b0000_0001

// wireTap records what crossed the wire for one procedure: the request
// headers and compressed size, plus the response headers and bytes.
type wireTap struct {
	procedure string

	mu       sync.Mutex
	reqHdr   http.Header
	reqBytes int
	respHdr  http.Header
	respBody bytes.Buffer
}

func newTapServer(t *testing.T, h http.Handler, tap *wireTap) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(WithEscrowID(r.Context(), testEscrowID))
		if tap == nil || r.URL.Path != tap.procedure {
			h.ServeHTTP(w, r)
			return
		}
		counted := &countingBody{ReadCloser: r.Body}
		r.Body = counted
		h.ServeHTTP(&tapWriter{ResponseWriter: w, tap: tap}, r)
		tap.mu.Lock()
		tap.reqHdr = r.Header.Clone()
		tap.reqBytes = counted.n
		tap.respHdr = w.Header().Clone()
		tap.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return srv
}

type countingBody struct {
	io.ReadCloser
	n int
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n += n
	return n, err
}

type tapWriter struct {
	http.ResponseWriter
	tap *wireTap
}

func (w *tapWriter) Write(p []byte) (int, error) {
	w.tap.mu.Lock()
	w.tap.respBody.Write(p)
	w.tap.mu.Unlock()
	return w.ResponseWriter.Write(p)
}

func (w *tapWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *wireTap) requestEncoding() string  { return w.encoding(&w.reqHdr) }
func (w *wireTap) responseEncoding() string { return w.encoding(&w.respHdr) }

// encoding is unary Content-Encoding or the Connect streaming equivalent,
// whichever the protocol used.
func (w *wireTap) encoding(h *http.Header) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if enc := h.Get("Content-Encoding"); enc != "" {
		return enc
	}
	return h.Get("Connect-Content-Encoding")
}

func (w *wireTap) requestBytes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.reqBytes
}

// responseEnvelopeFlags is the prefix flag byte of every Connect envelope the
// handler wrote.
func (w *wireTap) responseEnvelopeFlags(t *testing.T) []byte {
	t.Helper()
	w.mu.Lock()
	body := append([]byte(nil), w.respBody.Bytes()...)
	w.mu.Unlock()
	var flags []byte
	for len(body) > 0 {
		require.GreaterOrEqual(t, len(body), 5, "truncated connect envelope")
		size := int(binary.BigEndian.Uint32(body[1:5]))
		require.GreaterOrEqual(t, len(body), 5+size, "truncated connect envelope")
		flags = append(flags, body[0])
		body = body[5+size:]
	}
	require.NotEmpty(t, flags)
	return flags
}

type gossipCore struct {
	stubCore
	onNonce func(transport.GossipNonceRequest) error
}

func (g gossipCore) ServeGossipNonce(req transport.GossipNonceRequest) error {
	if g.onNonce != nil {
		return g.onNonce(req)
	}
	return nil
}

func (g gossipCore) ServeGossipTxs([]*types.DevshardTx) {}
