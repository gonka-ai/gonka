package rpcserver

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/internal/testutil"
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
	ran *atomic.Bool
}

func (c chatCore) ServeInference(context.Context, transport.InferenceCall) error {
	if c.ran != nil {
		c.ran.Store(true)
	}
	return nil
}

func TestChat_ReadCapAllowsOver16KiB(t *testing.T) {
	var ran atomic.Bool
	lookup := stubLookup{core: chatCore{stubCore: stubCore{}, ran: &ran}}
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

func TestChat_GzipBombRejectedBeforeHandler(t *testing.T) {
	var ran atomic.Bool
	lookup := stubLookup{core: chatCore{stubCore: stubCore{}, ran: &ran}}
	auth := newTestAuth(PeerAuthConfig{})
	mux := NewMux(auth, NewSessionHandler(lookup))
	srv := httptest.NewServer(withTestEscrow(mux))
	t.Cleanup(srv.Close)
	signer := testutil.MustGenerateKey(t)
	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer, []byte("chat-bomb-attach-nonce-0123"))

	env := &rpcpb.SignedEnvelope{
		Payload:   bytes.Repeat([]byte("n"), int(transport.DefaultMaxBodySize)+1),
		EscrowId:  testEscrowID,
		Timestamp: 1,
		Signature: []byte{1},
	}
	raw, err := proto.Marshal(env)
	require.NoError(t, err)
	require.Greater(t, len(raw), int(transport.DefaultMaxBodySize))
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err = zw.Write(raw)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.Less(t, buf.Len(), int(transport.DefaultMaxBodySize))

	req, err := http.NewRequest(http.MethodPost, srv.URL+rpcpbconnect.SessionServiceChatProcedure, bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Content-Encoding", "gzip")
	SetSessionHeader(req.Header, attached.SessionToken)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.NotEqual(t, http.StatusOK, resp.StatusCode)
	require.False(t, ran.Load(), "Chat gzip bomb / unknown gzip must not reach ServeInference")
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
