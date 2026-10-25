package protocol

import (
	"context"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/gossip"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport"
	"devshard/types"
)

func TestHTTP_MidSessionFailover_WideGapSnapshotCatchUpAndFinalize(t *testing.T) {
	env := setupHTTPEnv(t, 3, 10_000_000, 200)
	ctx := context.Background()
	params := defaultParams()

	// Advance past SnapshotCatchUpMinGap (64 nonces) so failover exercises
	// snapshot transfer + tail diff replay over HTTP transport.
	for i := 0; i < 69; i++ {
		res, err := env.session.SendInference(ctx, params)
		require.NoError(t, err, "inference %d", i+1)
		require.NotNil(t, res)
	}
	drainSessionPending(t, ctx, env.session)
	tipBeforeFailover := env.session.Nonce()
	require.Greater(t, tipBeforeFailover, host.SnapshotCatchUpMinGap)

	// Wait briefly so async gossip signatures settle onto peers 1 & 2.
	time.Sleep(100 * time.Millisecond)

	// Simulate Kubernetes / HAProxy pod replacement + total local DB loss on
	// Host 0: hot-swap httpServers[0]'s handler to a brand-new cold Host 0 at
	// nonce 0 while env.session still thinks Host 0 acked up to tipBeforeFailover.
	verifier := signing.NewSecp256k1Verifier()
	coldStore0 := storage.NewMemory()
	require.NoError(t, coldStore0.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		Config:         env.config,
		Group:          env.group,
		InitialBalance: 10_000_000,
		CreatorAddr:    env.userSigner.Address(),
	}))
	coldSM0, err := state.NewStateMachine(
		"escrow-1",
		env.config,
		env.group,
		10_000_000,
		env.userSigner.Address(),
		verifier,
		coldStore0,
	)
	require.NoError(t, err)
	coldHost0, err := host.NewHost(
		coldSM0,
		env.signers[0],
		stub.NewInferenceEngine(),
		"escrow-1",
		env.group,
		nil,
		host.WithGrace(200),
		host.WithStorage(coldStore0),
		host.WithVerifier(verifier),
	)
	require.NoError(t, err)
	t.Cleanup(coldHost0.Close)

	coldSrv0, err := transport.NewServer(coldHost0, coldStore0, verifier, env.userSigner.Address())
	require.NoError(t, err)

	e0 := echo.New()
	registerServer(e0.Group("/devshard/v2"), coldSrv0)
	env.httpServers[0].Config.Handler = e0

	coldPeers := make(map[int]*transport.HTTPClient, len(env.httpServers))
	for j := range env.httpServers {
		coldPeers[j] = httpTestClient(env.httpServers[j].URL, "escrow-1", env.signers[0])
	}
	coldSrv0.SetPeerClients(transport.HTTPPeerClients(coldPeers))

	var gossipPeers0 []gossip.PeerClient
	for j := 1; j < len(env.httpServers); j++ {
		gossipPeers0 = append(gossipPeers0, httpTestClient(env.httpServers[j].URL, "escrow-1", env.signers[0]))
	}
	g0 := gossip.NewGossip("escrow-1", 0, gossipPeers0, coldHost0.HostMempool(), gossip.WithSigAccumulator(coldHost0))
	coldSrv0.SetGossip(g0)

	// 1. Nil-diff Finalize-resume probe against coldHost0 jumps 0 -> 70 via peer snapshot.
	_, probeErr := env.clients[0].Send(ctx, host.HostRequest{
		Nonce: tipBeforeFailover,
	}, nil, nil)
	require.NoError(t, probeErr, "nil-diff probe to cold failover Host 0 must self-catch-up from peers")
	require.Equal(t, tipBeforeFailover, coldHost0.LatestNonce())

	snapNonce, _, snapErr := coldStore0.LoadSnapshot("escrow-1")
	require.NoError(t, snapErr, "wide-gap failover must persist adopted peer snapshot")
	require.Greater(t, snapNonce, uint64(0))

	// 2. Continue serving live inferences and settle via Finalize across all 3 hosts!
	for i := 0; i < 3; i++ {
		res, infErr := env.session.SendInference(ctx, params)
		require.NoError(t, infErr)
		require.NotNil(t, res)
	}
	drainSessionPending(t, ctx, env.session)

	require.NoError(t, env.session.Finalize(ctx))
	st := env.session.StateMachine().SnapshotState()
	require.Equal(t, types.PhaseSettlement, st.Phase)

	finalNonce := env.session.Nonce()
	time.Sleep(100 * time.Millisecond)

	merged := make(map[uint32][]byte)
	for _, c := range env.clients {
		sigs, sErr := c.GetSignatures(ctx, finalNonce)
		if sErr != nil {
			continue
		}
		for slotID, sig := range sigs {
			merged[slotID] = sig
		}
	}
	require.Len(t, merged, 3, "all 3 hosts including cold-failover Host 0 must sign settlement root")
}
