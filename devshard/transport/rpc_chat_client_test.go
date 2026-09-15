package transport_test

import (
	"bytes"
	"context"
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
	"devshard/transport/rpcserver"
	"devshard/types"
)

type silentChatCore struct{}

func (silentChatCore) ServeGetSignatures(uint64) (map[uint32][]byte, error) {
	return map[uint32][]byte{}, nil
}

func (silentChatCore) AllowsSender(string) bool { return true }

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
	httpSrv, _ := startPeerRPCServer(t, hostSigner.Address(), rpcserver.PeerAuthConfig{Heartbeat: time.Hour}, lookup)
	pc := newTestPeerConn(t, httpSrv, hostSigner.Address(), userSigner, transport.PeerConnConfig{})
	pc.Start()
	waitPeerReady(t, pc)
	rpc := transport.NewRPCClient(transport.NewHTTPClient(httpSrv.URL, "escrow-1", userSigner, cfg), pc, transport.ParseRPCEndpoints(transport.EndpointChat))
	return rpc, userSigner
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
