package transport_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/transport/rpcserver"
	"devshard/types"
)

type silentChatCore struct{}

func (silentChatCore) ServeGetSignatures(uint64) (map[uint32][]byte, error) {
	return map[uint32][]byte{}, nil
}

func (silentChatCore) AllowsSender(string) bool { return true }

func (silentChatCore) IsOwner(string) bool { return true }

func (silentChatCore) ServeInference(context.Context, transport.InferenceCall) error {
	return nil
}

type chatLookup struct{ core rpcserver.SessionCore }

func (c chatLookup) SessionServerExisting(string) (rpcserver.SessionCore, error) {
	return c.core, nil
}

func (c chatLookup) SessionForParticipant(string, string) (rpcserver.SessionCore, error) {
	return c.SessionServerExisting("")
}

func (c chatLookup) SessionForOwner(string, string) (rpcserver.SessionCore, error) {
	return c.SessionServerExisting("")
}

func TestRPCClient_SendTruncatedZeroFrames(t *testing.T) {
	hostAddr := testutil.MustGenerateKey(t).Address()
	peer := testutil.MustGenerateKey(t)
	srv, _ := startPeerRPCServer(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: time.Hour}, chatLookup{core: silentChatCore{}})
	pc := newTestPeerConn(t, srv, hostAddr, peer, transport.PeerConnConfig{})
	pc.Start()
	waitPeerReady(t, pc)
	rpc := transport.NewRPCClient(transport.NewHTTPClient(srv.URL, "escrow-1", peer), pc, transport.ParseRPCEndpoints(transport.EndpointChat))
	_, err := rpc.Send(context.Background(), host.HostRequest{
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:    []byte("x"),
			Model:     "llama",
			MaxTokens: 1,
			StartedAt: 1,
		},
	}, nil, nil)
	require.ErrorIs(t, err, transport.ErrSSEStreamTruncated)
}

func startLiveChatRPC(t *testing.T, opts ...transport.ServerOption) (*transport.RPCClient, *signing.Secp256k1Signer) {
	t.Helper()
	return startLiveChatRPCCfg(t, transport.DefaultClientConfig(), opts...)
}

func startLiveChatRPCCfg(t *testing.T, cfg transport.ClientConfig, opts ...transport.ServerOption) (*transport.RPCClient, *signing.Secp256k1Signer) {
	t.Helper()
	rpc, user, pc := startLiveChatRPCParts(t, cfg, opts...)
	pc.Start()
	waitPeerReady(t, pc)
	return rpc, user
}

func startLiveChatRPCParts(t *testing.T, cfg transport.ClientConfig, opts ...transport.ServerOption) (*transport.RPCClient, *signing.Secp256k1Signer, *transport.PeerConn) {
	t.Helper()
	return startLiveChatRPCPartsObserved(t, cfg, nil, opts...)
}

func startLiveChatRPCPartsObserved(t *testing.T, cfg transport.ClientConfig, obs *wireObserver, opts ...transport.ServerOption) (*transport.RPCClient, *signing.Secp256k1Signer, *transport.PeerConn) {
	t.Helper()
	return startLiveChatRPCPartsObservedCfg(t, cfg, obs, transport.PeerConnConfig{}, false, opts...)
}

func startLiveChatRPCPartsObservedCfg(t *testing.T, cfg transport.ClientConfig, obs *wireObserver, connCfg transport.PeerConnConfig, h2c bool, opts ...transport.ServerOption) (*transport.RPCClient, *signing.Secp256k1Signer, *transport.PeerConn) {
	t.Helper()
	hostSigner := testutil.MustGenerateKey(t)
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostSigner})
	config := testutil.DefaultConfig(1)
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userSigner.Address(), config, group, 100000))
	require.NoError(t, err)
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))
	h, err := host.NewHost(sm, hostSigner, stub.NewInferenceEngine(), "escrow-1", group, nil,
		host.WithGrace(100), host.WithStorage(store))
	require.NoError(t, err)
	t.Cleanup(h.Close)
	tsrv, err := transport.NewServer(h, store, verifier, userSigner.Address(), opts...)
	require.NoError(t, err)
	lookup := rpcserver.AdaptLookup(func(string) (*transport.Server, error) { return tsrv, nil })
	var mux *httptest.Server
	if h2c {
		httpSrv, _ := startPeerRPCServerH2CObserved(t, hostSigner.Address(), rpcserver.PeerAuthConfig{Heartbeat: time.Hour}, lookup, obs)
		mux = httpSrv
	} else {
		httpSrv, _ := startPeerRPCServerObserved(t, hostSigner.Address(), rpcserver.PeerAuthConfig{Heartbeat: time.Hour}, lookup, obs)
		mux = httpSrv
	}
	inf := mux
	if h2c {
		inf = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "must not use HTTP/1.1 when h2 works", http.StatusTeapot)
		}))
		t.Cleanup(inf.Close)
		connCfg.DialSet.H2URL = mux.URL
		if connCfg.H2ProbeTimeout == 0 {
			connCfg.H2ProbeTimeout = 200 * time.Millisecond
		}
	}
	if connCfg.DialSet.H2URL != "" {
		transport.ResetRPCH2MissCacheForTest()
		transport.ResetRPCH2ClientPoolForTest()
		t.Cleanup(transport.ResetRPCH2MissCacheForTest)
		t.Cleanup(transport.ResetRPCH2ClientPoolForTest)
	}
	pc := newTestPeerConn(t, inf, hostSigner.Address(), userSigner, connCfg)
	rpc := transport.NewRPCClient(transport.NewHTTPClient(inf.URL, "escrow-1", userSigner, cfg), pc, transport.ParseRPCEndpoints(transport.EndpointChat))
	return rpc, userSigner, pc
}

func chatHostRequest(t *testing.T, user *signing.Secp256k1Signer) host.HostRequest {
	t.Helper()
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	return host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   testutil.TestMaxTokens,
			StartedAt:   1000,
		},
	}
}

func TestRPCClient_SendWaitsForAttach(t *testing.T) {
	rpc, user, pc := startLiveChatRPCParts(t, transport.DefaultClientConfig())
	done := make(chan error, 1)
	go func() {
		_, err := rpc.Send(context.Background(), chatHostRequest(t, user), nil, nil)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Send returned before Attach: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	pc.Start()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Send did not finish after Attach")
	}
}

func TestRPCClient_SendRoundTrip(t *testing.T) {
	rpc, user := startLiveChatRPC(t)
	var stream bytes.Buffer
	resp, err := rpc.Send(context.Background(), chatHostRequest(t, user), &stream, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(1), resp.Nonce)
	require.NotNil(t, resp.Receipt)
	require.NotEmpty(t, resp.Mempool)
	out := stream.String()
	require.Contains(t, out, "stub")
	require.True(t, strings.Contains(out, "[DONE]") || strings.Contains(out, "stub"),
		"token stream should carry the stub completion")
}

func TestRPCClient_SendRoundTripGRPCOnH2(t *testing.T) {
	obs := newWireObserver()
	rpc, user, pc := startLiveChatRPCPartsObservedCfg(t, transport.DefaultClientConfig(), obs, transport.PeerConnConfig{GRPC: true}, true)
	pc.Start()
	waitPeerReady(t, pc)
	require.True(t, pc.UsingH2())
	require.True(t, pc.UsingGRPC())

	var stream bytes.Buffer
	resp, err := rpc.Send(context.Background(), chatHostRequest(t, user), &stream, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(1), resp.Nonce)
	require.NotNil(t, resp.Receipt)
	require.Contains(t, stream.String(), "stub")

	chat := rpcpbconnect.SessionServiceChatProcedure
	requireGRPCContentType(t, obs.requestContentType(rpcpbconnect.PeerAuthServiceAttachProcedure))
	requireGRPCContentType(t, obs.requestContentType(chat))
	require.Equal(t, "gzip", obs.requestEncoding(chat), "gRPC Chat envelope must still be gzipped")
}

func TestRPCClient_SendGRPCFallsBackToConnect(t *testing.T) {
	obs := newWireObserver()
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()
	rpc, user, pc := startLiveChatRPCPartsObservedCfg(t, transport.DefaultClientConfig(), obs, transport.PeerConnConfig{
		DialSet:        transport.PeerRPCDialSet{H2URL: dead.URL},
		GRPC:           true,
		H2ProbeTimeout: 200 * time.Millisecond,
	}, false)
	pc.Start()
	waitPeerReady(t, pc)
	require.False(t, pc.UsingH2())
	require.False(t, pc.UsingGRPC())

	var stream bytes.Buffer
	resp, err := rpc.Send(context.Background(), chatHostRequest(t, user), &stream, nil)
	require.NoError(t, err)
	require.NotNil(t, resp.Receipt)
	require.Contains(t, stream.String(), "stub")
	requireConnectContentType(t, obs.requestContentType(rpcpbconnect.PeerAuthServiceAttachProcedure))
	requireConnectContentType(t, obs.requestContentType(rpcpbconnect.SessionServiceChatProcedure))
}

func TestRPCClient_SendGzipsRequestAndKeepsFramesSingleGzip(t *testing.T) {
	obs := newWireObserver()
	rpc, user, pc := startLiveChatRPCPartsObserved(t, transport.DefaultClientConfig(), obs)
	pc.Start()
	waitPeerReady(t, pc)

	var stream bytes.Buffer
	resp, err := rpc.Send(context.Background(), chatHostRequest(t, user), &stream, nil)
	require.NoError(t, err)
	require.NotNil(t, resp.Receipt)
	require.Contains(t, stream.String(), "stub")

	chat := rpcpbconnect.SessionServiceChatProcedure
	require.Equal(t, "gzip", obs.requestEncoding(chat), "the prompt envelope must be gzipped on the wire")
	require.Equal(t, "gzip", obs.responseEncoding(chat),
		"negotiation still names gzip; ChatFrames carry the application gzip stream")
}

func TestRPCClient_SendHonorsInferenceTimeout(t *testing.T) {
	cfg := transport.DefaultClientConfig()
	cfg.InferenceTimeout = 150 * time.Millisecond
	rpc, user := startLiveChatRPCCfg(t, cfg, transport.WithReceiptDelay(time.Hour))
	start := time.Now()
	_, err := rpc.Send(context.Background(), chatHostRequest(t, user), nil, nil)
	require.Error(t, err)
	require.Less(t, time.Since(start), 2*time.Second)
}

func TestRPCClient_SendCancelReleasesHost(t *testing.T) {
	rpc, user := startLiveChatRPC(t, transport.WithReceiptDelay(time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := rpc.Send(ctx, chatHostRequest(t, user), nil, nil)
	require.Error(t, err)
	require.Less(t, time.Since(start), 500*time.Millisecond)
}
