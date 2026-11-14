package rpcserver

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"

	"common/httpguard"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
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
		member: true,
		diffs:  []types.DiffRecord{{Diff: types.Diff{Nonce: 1, UserSig: make([]byte, 8<<10)}}},
		mempool: []*types.DevshardTx{{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			PromptHash: bytes.Repeat([]byte{1}, transport.MinGzipBodyBytes+32),
		}}}},
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
	if enc := h.Get("Grpc-Encoding"); enc != "" {
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

// catchingCore records the ChallengeReceipt the handler was given.
type catchingCore struct {
	stubCore
	ran *atomic.Bool
	req transport.ChallengeReceiptRequest
}

func (c *catchingCore) ServeChallengeReceipt(_ context.Context, req transport.ChallengeReceiptRequest) (*transport.ChallengeReceiptResponse, error) {
	if c.ran != nil {
		c.ran.Store(true)
	}
	c.req = req
	return &transport.ChallengeReceiptResponse{}, nil
}

// newGzipRPC is an attached RPCClient aimed at a direct mux, plus a tap on
// procedure. grpc speaks native gRPC on an h2c server.
func newGzipRPC(t *testing.T, lookup SessionLookup, endpoints, procedure string, grpc bool, opts ...MuxOption) (*transport.RPCClient, *wireTap) {
	t.Helper()
	httpguard.SetAllowPrivate(true)
	tap := &wireTap{procedure: procedure}
	auth := newTestAuth(PeerAuthConfig{Heartbeat: 50 * time.Millisecond})
	t.Cleanup(auth.Close)
	mux := NewMux(auth, NewSessionHandler(lookup), opts...)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(WithEscrowID(r.Context(), testEscrowID))
		if r.URL.Path != tap.procedure {
			mux.ServeHTTP(w, r)
			return
		}
		counted := &countingBody{ReadCloser: r.Body}
		r.Body = counted
		mux.ServeHTTP(&tapWriter{ResponseWriter: w, tap: tap}, r)
		tap.mu.Lock()
		tap.reqHdr = r.Header.Clone()
		tap.reqBytes = counted.n
		tap.respHdr = w.Header().Clone()
		tap.mu.Unlock()
	})

	peer := testutil.MustGenerateKey(t)
	cfg := transport.PeerConnConfig{
		HostAddress:  testHostAddress,
		Signer:       peer,
		DirectMux:    true,
		DoorEscrowID: testEscrowID,
		WatchStale:   time.Minute,
	}
	var httpURL string
	if grpc {
		transport.ResetRPCH2MissCacheForTest()
		transport.ResetRPCH2ClientPoolForTest()
		t.Cleanup(transport.ResetRPCH2MissCacheForTest)
		t.Cleanup(transport.ResetRPCH2ClientPoolForTest)
		h2 := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
		t.Cleanup(h2.Close)
		inf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "must not use HTTP/1.1 when h2 works", http.StatusTeapot)
		}))
		t.Cleanup(inf.Close)
		cfg.BaseURL = inf.URL
		cfg.DialSet = transport.PeerRPCDialSet{H2URL: h2.URL}
		cfg.GRPC = true
		cfg.H2ProbeTimeout = 200 * time.Millisecond
		httpURL = inf.URL
	} else {
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)
		cfg.BaseURL = srv.URL
		httpURL = srv.URL
	}
	pc := transport.NewPeerConn(cfg)
	t.Cleanup(pc.Close)
	pc.Start()
	require.Eventually(t, func() bool { return pc.Ready() && len(pc.LiveToken()) > 0 }, 3*time.Second, 10*time.Millisecond)

	rpc := transport.NewRPCClient(
		transport.NewHTTPClient(httpURL, testEscrowID, peer, transport.DefaultClientConfig()),
		pc, transport.ParseRPCEndpoints(endpoints))
	t.Cleanup(rpc.Close)
	return rpc, tap
}

// catchUpWorld is one escrow the original diff and the diff the handler
// decoded are both applied to, so their roots are comparable.
type catchUpWorld struct {
	signers []*signing.Secp256k1Signer
	user    *signing.Secp256k1Signer
	group   []types.SlotAssignment
	config  types.SessionConfig
	diff    types.Diff
	stored  []byte
}

func newCatchUpWorld(t *testing.T) catchUpWorld {
	t.Helper()
	signers := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(signers)
	config := testutil.DefaultConfig(len(signers))
	preview, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)
	start := testutil.StartTx(1)
	start.GetStartInference().Model = strings.Repeat("m", transport.MinGzipBodyBytes)
	txs := []*types.DevshardTx{start}
	root, err := preview.ApplyLocal(1, txs)
	require.NoError(t, err)
	diff := testutil.SignDiffWithRoot(t, user, "escrow-1", 1, txs, root)
	stored, err := proto.Marshal(&types.DiffContent{Txs: diff.Txs})
	require.NoError(t, err)
	return catchUpWorld{signers: signers, user: user, group: group, config: config, diff: diff, stored: stored}
}

func (w catchUpWorld) root(t *testing.T, diff types.Diff) []byte {
	t.Helper()
	sm, err := state.NewStateMachine("escrow-1", w.config, w.group, 10000, w.user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", w.user.Address(), w.config, w.group, 10000))
	require.NoError(t, err)
	h, err := host.NewHost(sm, w.signers[0], stub.NewInferenceEngine(), "escrow-1", w.group, nil)
	require.NoError(t, err)
	t.Cleanup(h.Close)
	_, _, err = h.ChallengeReceipt(context.Background(), 1, nil, []types.Diff{diff})
	require.NoError(t, err)
	root, err := h.StateRoot()
	require.NoError(t, err)
	return root
}

func TestChallengeCatchUpPageIsGzipped(t *testing.T) {
	world := newCatchUpWorld(t)
	wire := types.DiffWire{
		Nonce:         world.diff.Nonce,
		Txs:           world.stored,
		UserSig:       world.diff.UserSig,
		PostStateRoot: world.diff.PostStateRoot,
	}
	for _, tc := range []struct {
		name string
		grpc bool
	}{
		{name: "connect", grpc: false},
		{name: "grpc", grpc: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := &catchingCore{stubCore: stubCore{member: true}}
			rpc, tap := newGzipRPC(t, stubLookup{core: core}, transport.EndpointChallengeReceipt,
				rpcpbconnect.SessionServiceChallengeReceiptProcedure, tc.grpc)
			require.NoError(t, rpc.ChallengeCatchUp(context.Background(), 1, []types.DiffWire{wire}, ""))
			require.Equal(t, "gzip", tap.requestEncoding())
			require.Len(t, core.req.Diffs, 1)
			require.Equal(t, transport.DiffWireTxs(wire.Nonce, world.stored), core.req.Diffs[0].Txs)

			decoded, err := transport.DiffsFromJSON(core.req.Diffs)
			require.NoError(t, err)
			require.Len(t, decoded, 1)
			require.Equal(t, world.root(t, world.diff), world.root(t, decoded[0]))
		})
	}
}

func TestChallengeCatchUpGzipInflateCap(t *testing.T) {
	var ran atomic.Bool
	core := &catchingCore{stubCore: stubCore{member: true}, ran: &ran}
	rpc, tap := newGzipRPC(t, stubLookup{core: core}, transport.EndpointChallengeReceipt,
		rpcpbconnect.SessionServiceChallengeReceiptProcedure, false)
	err := rpc.ChallengeCatchUp(context.Background(), 1, []types.DiffWire{{
		Nonce: 1,
		Txs:   bytes.Repeat([]byte{0}, transport.DefaultRPCLargeReadMaxBytes+1),
	}}, "")
	require.Error(t, err)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.False(t, ran.Load(), "a page that inflates past the read cap never reaches ServeChallengeReceipt")
	require.Less(t, tap.requestBytes(), transport.DefaultRPCLargeReadMaxBytes,
		"the compressed page fits; the reject is the inflate cap")
}

func TestCompressMinBytes(t *testing.T) {
	t.Run("under", func(t *testing.T) {
		rpc, tap := newGzipRPC(t, stubLookup{core: stubCore{
			member: true,
			sigs:   map[uint32][]byte{0: {1, 2, 3, 4}},
		}}, transport.EndpointSignatures, rpcpbconnect.SessionServiceGetSignaturesProcedure, false)
		got, err := rpc.GetSignatures(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, []byte{1, 2, 3, 4}, got[0])
		require.Empty(t, tap.requestEncoding())
		require.Empty(t, tap.responseEncoding())
	})
	t.Run("over", func(t *testing.T) {
		const n = transport.MinGzipBodyBytes + 256
		lookup := stubLookup{core: stubCore{member: true}}
		payload := NewPayloadHandler(lookup, func(_ context.Context, _ SessionCore, _ string, req *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
			return &rpcpb.GetPayloadResponse{
				InferenceId:   req.GetInferenceId(),
				PromptPayload: bytes.Repeat([]byte("p"), n),
			}, nil
		})
		rpc, tap := newGzipRPC(t, lookup, transport.EndpointPayload, rpcpbconnect.PayloadServiceGetPayloadProcedure, false,
			WithPayloadService(payload))
		resp, err := rpc.GetPayload(context.Background(), &rpcpb.GetPayloadRequest{
			InferenceId: "1",
			Signature:   bytes.Repeat([]byte("s"), n),
		}, 0)
		require.NoError(t, err)
		require.Len(t, resp.GetPromptPayload(), n)
		require.Equal(t, "gzip", tap.requestEncoding())
		require.Equal(t, "gzip", tap.responseEncoding())
	})
}

// gzipZeroMembers is a multi-member gzip body. Each member decodes to
// decoded zeros. Concatenated members are one stream to gzip.Reader.
func gzipZeroMembers(t *testing.T, decoded, members int) []byte {
	t.Helper()
	var one bytes.Buffer
	zw, err := gzip.NewWriterLevel(&one, gzip.BestSpeed)
	require.NoError(t, err)
	_, err = zw.Write(bytes.Repeat([]byte{0}, decoded))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.Greater(t, members, 1)
	return bytes.Repeat(one.Bytes(), members)
}

func postRPC(t *testing.T, srv *httptest.Server, procedure string, token, body []byte, hdr map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+procedure, bytes.NewReader(body))
	require.NoError(t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	SetSessionHeader(req.Header, token)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(raw)
}

func TestHandshakeGate_GzipBombStopsInflating(t *testing.T) {
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
	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer, []byte("gzip-stop-attach-nonce-0123"))

	// 8 MiB decoded, compressed well under the 16 KiB read cap. A full drain
	// would report "message size <decoded>". Stopping reports that the size
	// could not be determined.
	bomb := gzipZeroMembers(t, 1<<20, 8)
	require.Less(t, len(bomb), transport.DefaultRPCReadMaxBytes)
	status, body := postRPC(t, srv, rpcpbconnect.GossipServiceNonceProcedure, attached.SessionToken, bomb, map[string]string{
		"Content-Type":     "application/proto",
		"Content-Encoding": "gzip",
	})
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Contains(t, body, "unable to determine message size")
	require.False(t, ran.Load(), "gzip bomb must not reach ServeGossipNonce")
}

func TestGetPayload_GzipBombStopsInflating(t *testing.T) {
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
	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer, []byte("payload-stop-attach-nonce-01"))

	bomb := gzipZeroMembers(t, 1<<20, 12)
	require.Less(t, len(bomb), int(transport.DefaultMaxBodySize))
	status, body := postRPC(t, srv, rpcpbconnect.PayloadServiceGetPayloadProcedure, attached.SessionToken, bomb, map[string]string{
		"Content-Type":     "application/proto",
		"Content-Encoding": "gzip",
	})
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Contains(t, body, "unable to determine message size")
	require.False(t, ran.Load(), "gzip bomb must not reach ServeRPCGetPayload")
}

func TestChat_GzipBombStopsInflating(t *testing.T) {
	var ran atomic.Bool
	lookup := stubLookup{core: chatCore{stubCore: stubCore{owner: true}, ran: &ran}}
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, NewSessionHandler(lookup))))
	t.Cleanup(srv.Close)
	signer := testutil.MustGenerateKey(t)
	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer, []byte("chat-stop-attach-nonce-01234"))

	bomb := gzipZeroMembers(t, 1<<20, 12)
	require.Less(t, len(bomb), int(transport.DefaultMaxBodySize))
	var env bytes.Buffer
	env.WriteByte(connectFlagCompressed)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(bomb)))
	env.Write(size[:])
	env.Write(bomb)

	status, body := postRPC(t, srv, rpcpbconnect.SessionServiceChatProcedure, attached.SessionToken, env.Bytes(), map[string]string{
		"Content-Type":             "application/connect+proto",
		"Connect-Content-Encoding": "gzip",
		"Connect-Protocol-Version": "1",
	})
	require.Contains(t, body, "unable to determine message size", "status %d body %q", status, body)
	require.False(t, ran.Load(), "gzip bomb must not reach ServeInference")
}
