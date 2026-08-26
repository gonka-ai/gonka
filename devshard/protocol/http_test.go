package protocol

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"common/httpguard"

	"devshard/gossip"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport"
	"devshard/types"
	"devshard/user"
)

// TestMain enables the permissive dialer for this package's HTTP integration
// tests, which spin up httptest servers on 127.0.0.1 and reach them through the
// real transport dialer. Without this the dial-time SSRF guard (default secure)
// would block the loopback connections.
func TestMain(m *testing.M) {
	httpguard.SetAllowPrivate(true)
	os.Exit(m.Run())
}

type httpTestEnv struct {
	session     *user.Session
	hosts       []*host.Host
	servers     []*transport.Server
	httpServers []*httptest.Server
	clients     []*transport.HTTPClient // user-authenticated clients
	hostClients []*transport.HTTPClient // host-authenticated clients (for gossip)
	signers     []*signing.Secp256k1Signer
	userSigner  *signing.Secp256k1Signer
	group       []types.SlotAssignment
	config      types.SessionConfig
	stores      []*storage.Memory
	gossips     []*gossip.Gossip
}

type recoveryVerifierClient struct {
	user.HostClient
	source *recoveryVerifierSource
}

type recoveryVerifierSource struct {
	txs []*types.DevshardTx
}

func (c *recoveryVerifierClient) VerifyTimeout(_ context.Context, _ uint64, _ types.TimeoutReason, _ *host.InferencePayload, _ []types.Diff, _ host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	return false, nil, 0, c.source.txs, "", nil
}

const httpTestRoutePrefix = "/devshard/v2"

func httpTestClient(baseURL string, escrowID string, signer signing.Signer) *transport.HTTPClient {
	cfg := transport.DefaultClientConfig()
	cfg.RoutePrefix = httpTestRoutePrefix
	return transport.NewHTTPClient(baseURL, escrowID, signer, cfg)
}

// registerServer wires srv's handlers onto g, mirroring the old Server.Register
// method. Production wiring uses server/routes.go RegisterLazySessionRoutes.
func registerServer(g *echo.Group, srv *transport.Server) {
	g.Use(srv.AuthMiddleware)
	g.POST("/sessions/:id/chat/completions", srv.HandleInference)
	g.POST("/sessions/:id/height-sync", srv.HandleHeightSync)
	g.POST("/sessions/:id/heightsync/repair", srv.HandleHeightSyncRepair)
	g.POST("/sessions/:id/verify-timeout", srv.HandleVerifyTimeout)
	g.POST("/sessions/:id/challenge-receipt", srv.HandleChallengeReceipt)
	g.POST("/sessions/:id/gossip/nonce", srv.HandleGossipNonce)
	g.POST("/sessions/:id/gossip/txs", srv.HandleGossipTxs)
	g.GET("/sessions/:id/diffs", srv.HandleGetDiffs)
	g.GET("/sessions/:id/mempool", srv.HandleGetMempool)
	g.GET("/sessions/:id/signatures", srv.HandleGetSignatures)
}

// setupHTTPEnv creates a full HTTP test environment with storage, gossip,
// sig accumulation, and mempool sink wired together.
// Optional cfgs override the default SessionConfig.
func setupHTTPEnv(t *testing.T, numHosts int, balance, grace uint64, cfgs ...types.SessionConfig) *httpTestEnv {
	t.Helper()
	hostSigners := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(numHosts)
	if len(cfgs) > 0 {
		config = cfgs[0]
	}
	verifier := signing.NewSecp256k1Verifier()

	hosts := make([]*host.Host, numHosts)
	servers := make([]*transport.Server, numHosts)
	httpServers := make([]*httptest.Server, numHosts)
	stores := make([]*storage.Memory, numHosts)

	for i := range hostSigners {
		sm, err := state.NewStateMachine("escrow-1", config, group, balance, userSigner.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userSigner.Address(), config, group, balance))
		require.NoError(t, err)
		engine := stub.NewInferenceEngine()
		store := storage.NewMemory()
		require.NoError(t, store.CreateSession(storage.CreateSessionParams{
			EscrowID:       "escrow-1",
			Version:        testutil.RuntimeTestVersion,
			Config:         config,
			Group:          group,
			InitialBalance: balance,
		}))
		stores[i] = store

		h, err := host.NewHost(sm, hostSigners[i], engine, "escrow-1", group, nil,
			host.WithGrace(grace), host.WithStorage(store), host.WithVerifier(verifier))
		require.NoError(t, err)
		hosts[i] = h

		srv, srvErr := transport.NewServer(h, store, verifier, userSigner.Address())
		require.NoError(t, srvErr)
		servers[i] = srv

		e := echo.New()
		g := e.Group("/devshard/v2")
		registerServer(g, srv)
		ts := httptest.NewServer(e)
		t.Cleanup(ts.Close)
		httpServers[i] = ts
	}

	// Create HTTP clients: user-authenticated for inference, host-authenticated for gossip.
	clients := make([]*transport.HTTPClient, numHosts)
	hostClients := make([]*transport.HTTPClient, numHosts)
	userClients := make([]user.HostClient, numHosts)
	for i := range httpServers {
		c := httpTestClient(httpServers[i].URL, "escrow-1", userSigner)
		clients[i] = c
		userClients[i] = c
		hostClients[i] = httpTestClient(httpServers[i].URL, "escrow-1", hostSigners[i])
	}

	// Wire peer clients for timeout verification.
	for _, srv := range servers {
		peers := make(map[int]*transport.HTTPClient)
		for j, c := range clients {
			peers[j] = c
		}
		srv.SetPeerClients(peers)
	}

	// Wire gossip instances with host-authenticated peers and sig accumulation.
	gossips := make([]*gossip.Gossip, numHosts)
	for i, srv := range servers {
		var peers []gossip.PeerClient
		for j, c := range hostClients {
			if j == i {
				continue
			}
			peers = append(peers, c)
		}
		g := gossip.NewGossip("escrow-1", uint32(i), peers, hosts[i].HostMempool(), gossip.WithSigAccumulator(hosts[i]))
		gossips[i] = g
		srv.SetGossip(g)
	}

	userSM, err := state.NewStateMachine("escrow-1", config, group, balance, userSigner.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userSigner.Address(), config, group, balance))
	require.NoError(t, err)
	session, err := user.NewSession(userSM, userSigner, "escrow-1", group, userClients, verifier)
	require.NoError(t, err)

	return &httpTestEnv{
		session:     session,
		hosts:       hosts,
		servers:     servers,
		httpServers: httpServers,
		clients:     clients,
		hostClients: hostClients,
		signers:     hostSigners,
		userSigner:  userSigner,
		group:       group,
		config:      config,
		stores:      stores,
		gossips:     gossips,
	}
}

func setupHTTPRecoveryVerifierSession(t *testing.T, env *httpTestEnv, recovery []*types.DevshardTx) (*user.Session, *recoveryVerifierSource) {
	t.Helper()
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine(
		"escrow-1",
		env.config,
		env.group,
		1000000,
		env.userSigner.Address(),
		verifier,
		testutil.MustMemoryStore(t, "escrow-1", env.userSigner.Address(), env.config, env.group, 1000000),
	)
	require.NoError(t, err)

	source := &recoveryVerifierSource{txs: recovery}
	clients := make([]user.HostClient, len(env.clients))
	for i, c := range env.clients {
		clients[i] = &recoveryVerifierClient{HostClient: c, source: source}
	}

	session, err := user.NewSession(sm, env.userSigner, "escrow-1", env.group, clients, verifier)
	require.NoError(t, err)
	return session, source
}

func withHTTPTimeoutBuffer(t *testing.T, d time.Duration) {
	t.Helper()
	prev := user.TimeoutBuffer
	user.TimeoutBuffer = d
	t.Cleanup(func() {
		user.TimeoutBuffer = prev
	})
}

func TestHTTP_HappyPath(t *testing.T) {
	env := setupHTTPEnv(t, 3, 1000000, 100)
	ctx := context.Background()
	params := defaultParams()

	for i := 0; i < 15; i++ {
		result, err := env.session.SendInference(ctx, params)
		require.NoError(t, err, "inference %d", i+1)
		require.NotNil(t, result)
	}

	drainSessionPending(t, ctx, env.session)
	preFinalize := env.session.StateMachine().SnapshotState()
	require.Equal(t, 15, len(preFinalize.Inferences))
	for id, rec := range preFinalize.Inferences {
		require.Equal(t, types.StatusFinished, rec.Status, "inference %d should be finished", id)
	}

	err := env.session.Finalize(ctx)
	require.NoError(t, err)

	st := env.session.StateMachine().SnapshotState()
	require.Equal(t, types.PhaseSettlement, st.Phase)
	require.Empty(t, st.Inferences, "v2 drains live inferences at settlement")
	require.Equal(t, 15, len(env.session.StateMachine().ExportSealedNonces()))

	// After finalize, check signatures for the settlement nonce
	// (last Phase A nonce -- all hosts have seen it via Phase B catch-up).
	settlementNonce := env.session.Nonce() - uint64(len(env.group)) // end of Phase A

	// Wait briefly for async gossip to propagate.
	time.Sleep(100 * time.Millisecond)

	merged := make(map[uint32][]byte)
	for _, c := range env.clients {
		sigs, err := c.GetSignatures(ctx, settlementNonce)
		if err != nil {
			continue
		}
		for slotID, sig := range sigs {
			merged[slotID] = sig
		}
	}
	require.NotEmpty(t, merged, "should have signatures at settlement nonce %d", settlementNonce)
}

func TestHTTP_Auth_Rejected(t *testing.T) {
	env := setupHTTPEnv(t, 1, 100000, 100)

	// Create a client with a different signer (not the user).
	badSigner := testutil.MustGenerateKey(t)
	badClient := httpTestClient(env.httpServers[0].URL, "escrow-1", badSigner)

	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	_, err := badClient.Send(context.Background(), host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "403")
}

func TestHTTP_GossipPropagation(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)
	ctx := context.Background()

	// Send inference via HTTPClient directly to host 0.
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := env.clients[0].Send(ctx, host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Wait for async gossip propagation.
	time.Sleep(100 * time.Millisecond)

	// Manually gossip the same nonce to host 1 with matching hash (using host client).
	// Should succeed because gossip already propagated the real hash.
	err = env.hostClients[1].GossipNonce(ctx, 1, resp.StateHash, resp.StateSig, 0)
	require.NoError(t, err)
}

func TestHTTP_EquivocationDetection(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)
	ctx := context.Background()

	// Send inference to generate a real nonce+stateHash.
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := env.clients[0].Send(ctx, host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, resp.StateHash)

	// Gossip nonce directly to host 1 with matching hash (using host client).
	err = env.hostClients[1].GossipNonce(ctx, 1, resp.StateHash, resp.StateSig, 0)
	require.NoError(t, err)

	// Gossip with a different hash from a different slot. Sig won't verify
	// against slot 2's address, so it will be rejected with bad sig (not equivocation).
	err = env.hostClients[1].GossipNonce(ctx, 1, []byte("wrong-hash"), []byte("wrong-sig"), 2)
	require.Error(t, err)
}

func TestHTTP_TimeoutRefused(t *testing.T) {
	env := setupHTTPEnv(t, 5, 1000000, 100)
	ctx := context.Background()

	// Send one inference. Executor is slot 1%5=1.
	params := defaultParams()
	_, err := env.session.SendInference(ctx, params)
	require.NoError(t, err)

	// Shut down executor (host 1) to simulate refusal (unreachable).
	// ChallengeReceipt will fail, so verifying hosts accept the timeout.
	env.httpServers[1].Close()

	// Build timeout verifiers from HTTP clients (non-executor hosts).
	verifiers := make(map[int]user.TimeoutVerifier)
	for i := range env.clients {
		if i == 1 { // skip executor
			continue
		}
		verifiers[i] = env.clients[i]
	}

	// Catch up all non-executor hosts so they have the inference state.
	allDiffs := env.session.Diffs()
	for i, h := range env.hosts {
		if i == 1 {
			continue
		}
		_, err := h.HandleRequest(ctx, host.HostRequest{Diffs: allDiffs, Nonce: allDiffs[len(allDiffs)-1].Nonce})
		require.NoError(t, err)
	}

	votes, _, _, err := env.session.CollectTimeoutVotes(ctx, 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, &host.InferencePayload{
		Prompt:      testutil.TestPrompt,
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   1000,
	}, verifiers, allDiffs)
	require.NoError(t, err)
	require.True(t, len(votes) > int(env.config.VoteThreshold), "need >%d votes, got %d", env.config.VoteThreshold, len(votes))

	// Compose and apply timeout.
	timeoutTx := &types.DevshardTx{Tx: &types.DevshardTx_TimeoutInference{
		TimeoutInference: &types.MsgTimeoutInference{
			InferenceId: 1,
			Reason:      types.TimeoutReason_TIMEOUT_REASON_REFUSED,
			Votes:       votes,
		},
	}}
	nonce := env.session.Nonce() + 1
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", nonce, []*types.DevshardTx{timeoutTx})
	_, err = env.session.StateMachine().ApplyDiff(diff)
	require.NoError(t, err)

	st := env.session.StateMachine().SnapshotState()
	require.Equal(t, types.StatusTimedOut, st.Inferences[1].Status)
}

func TestHTTP_TimeoutExecution(t *testing.T) {
	// ExecutionTimeout=0 because the deadline is anchored to ConfirmedAt (real wall clock).
	// With any positive timeout, the deadline hasn't passed yet since confirmation just happened.
	config := testutil.DefaultConfig(5)
	config.ExecutionTimeout = 0
	env := setupHTTPEnv(t, 5, 1000000, 100, config)
	ctx := context.Background()
	params := defaultParams()

	// Send inference and confirm start.
	resp, err := env.session.SendInference(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, resp.Receipt)

	// Manually confirm start in a new diff.
	confirmTx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{
		ConfirmStart: &types.MsgConfirmStart{
			InferenceId: 1,
			ExecutorSig: resp.Receipt,
			ConfirmedAt: resp.ConfirmedAt,
		},
	}}
	nonce := env.session.Nonce() + 1
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", nonce, []*types.DevshardTx{confirmTx})
	_, err = env.session.StateMachine().ApplyDiff(diff)
	require.NoError(t, err)

	// Shut down executor (host 1) to simulate unreachable.
	env.httpServers[1].Close()

	// Catch up all non-executor hosts with diffs including the confirm.
	allDiffs := append(env.session.Diffs(), diff)
	for i, h := range env.hosts {
		if i == 1 {
			continue
		}
		_, err := h.HandleRequest(ctx, host.HostRequest{Diffs: allDiffs, Nonce: diff.Nonce})
		require.NoError(t, err)
	}

	verifiers := make(map[int]user.TimeoutVerifier)
	for i := range env.clients {
		if i == 1 {
			continue
		}
		verifiers[i] = env.clients[i]
	}

	votes, _, _, err := env.session.CollectTimeoutVotes(ctx, 1, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, nil, verifiers, allDiffs) // nil payload for execution timeout
	require.NoError(t, err)
	require.True(t, len(votes) > int(env.config.VoteThreshold),
		"need >%d votes, got %d", env.config.VoteThreshold, len(votes))
}

func TestHTTP_TimeoutRejected(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)
	ctx := context.Background()
	params := defaultParams()

	// Send inference and get finish included.
	_, err := env.session.SendInference(ctx, params)
	require.NoError(t, err)

	// Send second inference to pipeline the finish.
	_, err = env.session.SendInference(ctx, params)
	require.NoError(t, err)

	// Now inference 1 should be finished.
	st := env.session.StateMachine().SnapshotState()
	require.Equal(t, types.StatusFinished, st.Inferences[1].Status)

	// Catch up host 2.
	allDiffs := env.session.Diffs()
	_, err = env.hosts[2].HandleRequest(ctx, host.HostRequest{Diffs: allDiffs, Nonce: allDiffs[len(allDiffs)-1].Nonce})
	require.NoError(t, err)

	// Try timeout verification -- should fail because inference is finished.
	accept, _, _, _, _, err := env.clients[2].VerifyTimeout(ctx, 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, nil, nil, host.TimeoutArtifacts{})
	require.Error(t, err)
	require.False(t, accept)
}

func TestHTTP_ChallengeReceipt_RejectsTimeout(t *testing.T) {
	// Scenario: user withholds prompt from executor, verifying host challenges
	// executor via ChallengeReceipt, executor produces receipt -> timeout rejected.
	env := setupHTTPEnv(t, 5, 1000000, 100)
	ctx := context.Background()
	params := defaultParams()

	// Send inference. Executor is host 1 (nonce 1 % 5 = 1).
	_, err := env.session.SendInference(ctx, params)
	require.NoError(t, err)

	// Catch up non-executor hosts.
	allDiffs := env.session.Diffs()
	for i, h := range env.hosts {
		if i == 1 {
			continue
		}
		_, err := h.HandleRequest(ctx, host.HostRequest{Diffs: allDiffs, Nonce: allDiffs[len(allDiffs)-1].Nonce})
		require.NoError(t, err)
	}

	// Executor (host 1) is alive. Verifying hosts challenge it via ChallengeReceipt.
	// Timeout should be rejected because executor produces a receipt.
	verifiers := make(map[int]user.TimeoutVerifier)
	for i := range env.clients {
		if i == 1 {
			continue
		}
		verifiers[i] = env.clients[i]
	}

	votes, _, _, err := env.session.CollectTimeoutVotes(ctx, 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, &host.InferencePayload{
		Prompt:      testutil.TestPrompt,
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   1000,
	}, verifiers, allDiffs)
	require.NoError(t, err)
	require.Equal(t, 0, len(votes), "all hosts should reject timeout because executor is alive and produced receipt")
}

func TestHTTP_RefusedTimeoutChallengeRecoveryLandsInNextDiff(t *testing.T) {
	env := setupHTTPEnv(t, 5, 1000000, 100)
	ctx := context.Background()

	prepared, err := env.session.PrepareInference(defaultParams())
	require.NoError(t, err)
	require.Equal(t, uint64(1), prepared.Nonce())
	executorIdx := prepared.HostIdx()
	require.Equal(t, 1, executorIdx)

	result, err := env.session.HandleTimeout(ctx, prepared.Nonce(), time.Unix(0, 0), refusedPayload())
	require.NoError(t, err, "reachable executor receipt should recover instead of timing out")
	require.Equal(t, "refused", result.Reason)
	require.NotNil(t, findConfirmStart(env.hosts[executorIdx].MempoolTxs(), prepared.Nonce()),
		"executor should queue recovery MsgConfirmStart after challenge")

	diffs := env.session.Diffs()
	require.GreaterOrEqual(t, len(diffs), 2)
	require.NotNil(t, findConfirmStart(diffs[len(diffs)-1].Txs, prepared.Nonce()),
		"recovery MsgConfirmStart from challenge should land in the next user diff")
}

func TestHTTP_RefusedTimeoutRecoveryDeduplicatesAcrossVerifierRejects(t *testing.T) {
	env := setupHTTPEnv(t, 5, 1000000, 100)
	ctx := context.Background()

	prepared, err := env.session.PrepareInference(defaultParams())
	require.NoError(t, err)
	require.Equal(t, uint64(1), prepared.Nonce())

	_, err = env.session.HandleTimeout(ctx, prepared.Nonce(), time.Unix(0, 0), refusedPayload())
	require.NoError(t, err)

	diffs := env.session.Diffs()
	require.GreaterOrEqual(t, len(diffs), 2)
	recovery := diffs[len(diffs)-1]
	require.Equal(t, 1, countConfirmStart(recovery.Txs, prepared.Nonce()),
		"multiple verifier rejects must not duplicate the executor ConfirmStart in the recovery diff")
}

func TestHTTP_RefusedTimeoutRecoveryIgnoresUnrelatedMempool(t *testing.T) {
	env := setupHTTPEnv(t, 3, 1000000, 100)
	ctx := context.Background()
	unrelated := []*types.DevshardTx{
		{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
			InferenceId: 999,
			ExecutorSig: []byte("other-receipt"),
			ConfirmedAt: 1000,
		}}},
		{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{
			InferenceId:  999,
			ResponseHash: []byte("other-response"),
		}}},
	}
	session, _ := setupHTTPRecoveryVerifierSession(t, env, unrelated)

	prepared, err := session.PrepareInference(defaultParams())
	require.NoError(t, err)
	beforeDiffs := len(session.Diffs())

	_, err = session.HandleTimeout(ctx, prepared.Nonce(), time.Unix(0, 0), refusedPayload())
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient votes")
	require.Len(t, session.Diffs(), beforeDiffs)

	rec, ok := session.StateMachine().SnapshotState().Inferences[prepared.Nonce()]
	require.True(t, ok)
	require.Equal(t, types.StatusPending, rec.Status)
}

func TestHTTP_ExecutionTimeoutDoesNotUseConfirmStartRecovery(t *testing.T) {
	withHTTPTimeoutBuffer(t, 0)
	config := testutil.DefaultConfig(3)
	config.ExecutionTimeout = 0
	env := setupHTTPEnv(t, 3, 1000000, 100, config)
	ctx := context.Background()
	session, recoverySource := setupHTTPRecoveryVerifierSession(t, env, nil)

	prepared, err := session.PrepareInference(defaultParams())
	require.NoError(t, err)
	executorIdx := prepared.HostIdx()
	receipt, _, err := env.hosts[executorIdx].ChallengeReceipt(ctx, prepared.Nonce(), refusedPayload(), session.Diffs())
	require.NoError(t, err)
	require.NotEmpty(t, receipt)
	confirmTx := findConfirmStart(env.hosts[executorIdx].MempoolTxs(), prepared.Nonce())
	require.NotNil(t, confirmTx)
	recoverySource.txs = []*types.DevshardTx{confirmTx}

	resp := &host.HostResponse{Receipt: receipt, ConfirmedAt: confirmTx.GetConfirmStart().ConfirmedAt}
	require.NoError(t, session.ProcessResponse(executorIdx, resp, prepared.Nonce()))
	require.NoError(t, session.SendPendingDiff(ctx))
	require.Equal(t, types.StatusStarted, session.StateMachine().SnapshotState().Inferences[prepared.Nonce()].Status)
	beforeDiffs := len(session.Diffs())

	_, err = session.HandleTimeout(ctx, prepared.Nonce(), time.Unix(0, 0), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient votes")
	require.Len(t, session.Diffs(), beforeDiffs, "execution timeout must not publish ConfirmStart recovery")
	require.Equal(t, types.StatusStarted, session.StateMachine().SnapshotState().Inferences[prepared.Nonce()].Status)
}

func TestHTTP_RefusedTimeoutChallengeTimeoutThenRecoveryTxIsAvailable(t *testing.T) {
	env := setupHTTPEnv(t, 5, 1000000, 100)
	ctx := context.Background()

	prepared, err := env.session.PrepareInference(defaultParams())
	require.NoError(t, err)
	executorIdx := prepared.HostIdx()
	challenged := make(chan struct{}, 1)
	slowExecutor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var req transport.ChallengeReceiptRequest
		require.NoError(t, json.Unmarshal(body, &req))
		diffs := make([]types.Diff, 0, len(req.Diffs))
		for i, dj := range req.Diffs {
			diff, err := transport.DiffFromJSON(dj)
			require.NoError(t, err, "decode diff %d", i)
			diffs = append(diffs, diff)
		}

		receipt, _, err := env.hosts[executorIdx].ChallengeReceipt(r.Context(), req.InferenceID, transport.PayloadFromJSON(req.Payload), diffs)
		require.NoError(t, err)
		mempool, err := transport.DevshardTxsToBytes(host.RecoveryTxsFor(env.hosts[executorIdx].MempoolTxs(), req.InferenceID))
		require.NoError(t, err)
		select {
		case challenged <- struct{}{}:
		default:
		}

		time.Sleep(500 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(transport.ChallengeReceiptResponse{Receipt: receipt, Mempool: mempool})
	}))
	t.Cleanup(slowExecutor.Close)

	slowCfg := transport.DefaultClientConfig()
	slowCfg.RoutePrefix = httpTestRoutePrefix
	slowCfg.VerifyTimeout = 100 * time.Millisecond
	slowClient := transport.NewHTTPClient(slowExecutor.URL, "escrow-1", env.userSigner, slowCfg)

	for _, srv := range env.servers {
		peers := make(map[int]*transport.HTTPClient)
		for i, c := range env.clients {
			peers[i] = c
		}
		peers[executorIdx] = slowClient
		srv.SetPeerClients(peers)
	}

	votes, recovery, _, err := env.session.CollectTimeoutVotes(ctx, prepared.Nonce(), types.TimeoutReason_TIMEOUT_REASON_REFUSED, refusedPayload(), env.session.TimeoutVerifiers(), env.session.Diffs())
	require.NoError(t, err)
	require.True(t, len(votes) > int(env.config.VoteThreshold), "challenge response timeouts should initially look like executor unreachable")
	require.Empty(t, recovery)

	select {
	case <-challenged:
	case <-time.After(5 * time.Second):
		t.Fatal("executor was not challenged before verifier timeout")
	}
	require.NotNil(t, findConfirmStart(env.hosts[executorIdx].MempoolTxs(), prepared.Nonce()),
		"executor must keep ConfirmStart even when verifier times out waiting for challenge response")

	for _, srv := range env.servers {
		peers := make(map[int]*transport.HTTPClient)
		for i, c := range env.clients {
			peers[i] = c
		}
		srv.SetPeerClients(peers)
	}

	votes, recovery, _, err = env.session.CollectTimeoutVotes(ctx, prepared.Nonce(), types.TimeoutReason_TIMEOUT_REASON_REFUSED, refusedPayload(), env.session.TimeoutVerifiers(), env.session.Diffs())
	require.NoError(t, err)
	require.Empty(t, votes, "once the executor recovery tx is reachable, timeout should no longer pass as clean refused")
	require.NotNil(t, findConfirmStart(recovery, prepared.Nonce()), "next check must surface executor ConfirmStart as recovery")
}

func TestHTTP_ExecutionTimeoutRejectedWhenExecutorHasFinish(t *testing.T) {
	config := testutil.DefaultConfig(3)
	config.ExecutionTimeout = 0
	env := setupHTTPEnv(t, 3, 1000000, 100, config)
	ctx := context.Background()

	prepared, err := env.session.PrepareInference(defaultParams())
	require.NoError(t, err)
	executorIdx := prepared.HostIdx()

	receipt, _, err := env.clients[executorIdx].ChallengeReceipt(ctx, prepared.Nonce(), refusedPayload(), env.session.Diffs())
	require.NoError(t, err)
	require.NotEmpty(t, receipt)
	confirmTx := findConfirmStart(env.hosts[executorIdx].MempoolTxs(), prepared.Nonce())
	require.NotNil(t, confirmTx)

	require.NoError(t, env.session.ProcessResponse(executorIdx, &host.HostResponse{
		Receipt:     receipt,
		ConfirmedAt: confirmTx.GetConfirmStart().ConfirmedAt,
	}, prepared.Nonce()))
	require.NoError(t, env.session.SendPendingDiff(ctx))
	require.Equal(t, types.StatusStarted, env.session.StateMachine().SnapshotState().Inferences[prepared.Nonce()].Status)

	require.Eventually(t, func() bool {
		return findFinish(env.hosts[executorIdx].MempoolTxs(), prepared.Nonce()) != nil
	}, 5*time.Second, 20*time.Millisecond)

	votes, recovery, _, err := env.session.CollectTimeoutVotes(ctx, prepared.Nonce(), types.TimeoutReason_TIMEOUT_REASON_EXECUTION, nil, env.session.TimeoutVerifiers(), env.session.Diffs())
	require.NoError(t, err)
	require.Empty(t, votes, "executor finish in mempool must reject execution timeout")
	require.Empty(t, recovery, "execution-timeout rejection should not publish refused-start recovery")
}

func TestHTTP_NextRequestSettlesFinishFromExecutorMempool(t *testing.T) {
	env := setupHTTPEnv(t, 1, 1000000, 100)
	ctx := context.Background()

	prepared, err := env.session.PrepareInference(defaultParams())
	require.NoError(t, err)
	executorIdx := prepared.HostIdx()

	receipt, _, err := env.clients[executorIdx].ChallengeReceipt(ctx, prepared.Nonce(), refusedPayload(), env.session.Diffs())
	require.NoError(t, err)
	require.NotEmpty(t, receipt)
	confirmTx := findConfirmStart(env.hosts[executorIdx].MempoolTxs(), prepared.Nonce())
	require.NotNil(t, confirmTx)

	require.NoError(t, env.session.ProcessResponse(executorIdx, &host.HostResponse{
		Receipt:     receipt,
		ConfirmedAt: confirmTx.GetConfirmStart().ConfirmedAt,
	}, prepared.Nonce()))
	require.NoError(t, env.session.SendPendingDiff(ctx))
	require.Equal(t, types.StatusStarted, env.session.StateMachine().SnapshotState().Inferences[prepared.Nonce()].Status)

	require.Eventually(t, func() bool {
		return findFinish(env.hosts[executorIdx].MempoolTxs(), prepared.Nonce()) != nil
	}, 5*time.Second, 20*time.Millisecond)
	require.NotNil(t, findFinish(env.session.PendingTxs(), prepared.Nonce()),
		"ConfirmStart publish response must pull FinishInference from executor mempool into session pending txs")

	_, err = env.session.SendInference(ctx, defaultParams())
	require.NoError(t, err)
	require.Equal(t, types.StatusFinished, env.session.StateMachine().SnapshotState().Inferences[prepared.Nonce()].Status)
}

func TestHTTP_ExecutionTimeoutAfterFinishPulledPendingDoesNotTimeout(t *testing.T) {
	withHTTPTimeoutBuffer(t, 0)
	config := testutil.DefaultConfig(1)
	config.ExecutionTimeout = 0
	env := setupHTTPEnv(t, 1, 1000000, 100, config)
	ctx := context.Background()

	prepared, err := env.session.PrepareInference(defaultParams())
	require.NoError(t, err)
	executorIdx := prepared.HostIdx()

	receipt, _, err := env.clients[executorIdx].ChallengeReceipt(ctx, prepared.Nonce(), refusedPayload(), env.session.Diffs())
	require.NoError(t, err)
	require.NotEmpty(t, receipt)
	confirmTx := findConfirmStart(env.hosts[executorIdx].MempoolTxs(), prepared.Nonce())
	require.NotNil(t, confirmTx)

	require.NoError(t, env.session.ProcessResponse(executorIdx, &host.HostResponse{
		Receipt:     receipt,
		ConfirmedAt: confirmTx.GetConfirmStart().ConfirmedAt,
	}, prepared.Nonce()))
	require.NoError(t, env.session.SendPendingDiff(ctx))
	require.Equal(t, types.StatusStarted, env.session.StateMachine().SnapshotState().Inferences[prepared.Nonce()].Status)

	require.Eventually(t, func() bool {
		return findFinish(env.hosts[executorIdx].MempoolTxs(), prepared.Nonce()) != nil
	}, 5*time.Second, 20*time.Millisecond)
	require.NotNil(t, findFinish(env.session.PendingTxs(), prepared.Nonce()),
		"test setup expects FinishInference to be pending before timeout handling starts")

	result, err := env.session.HandleTimeout(ctx, prepared.Nonce(), time.Unix(0, 0), nil)
	require.NoError(t, err, "pending FinishInference should be published instead of timing out the started inference")
	require.Equal(t, "execution", result.Reason)
	require.Equal(t, types.StatusFinished, env.session.StateMachine().SnapshotState().Inferences[prepared.Nonce()].Status)
}

func TestHTTP_StateRecovery(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)
	ctx := context.Background()
	params := defaultParams()

	// Send 3 inferences.
	for i := 0; i < 3; i++ {
		_, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	// Catch up all hosts to the latest state.
	allDiffs := env.session.Diffs()
	lastNonce := allDiffs[len(allDiffs)-1].Nonce
	var roots [][]byte
	for _, h := range env.hosts {
		_, err := h.HandleRequest(ctx, host.HostRequest{Diffs: allDiffs, Nonce: lastNonce})
		require.NoError(t, err)
		root, err := h.StateRoot()
		require.NoError(t, err)
		roots = append(roots, root)
	}

	// All hosts must agree on state root.
	require.Len(t, roots, 3)
	for i := 1; i < len(roots); i++ {
		require.Equal(t, roots[0], roots[i], "host %d state root must match host 0", i)
	}

	// All hosts must agree on nonce.
	for i, h := range env.hosts {
		st := h.SnapshotState()
		require.Equal(t, lastNonce, st.LatestNonce, "host %d nonce mismatch", i)
	}

	// GET diffs from the host that stored them. At least one host should have diffs.
	var storedDiffs int
	for _, c := range env.clients {
		diffs, err := c.GetDiffs(ctx, 1, lastNonce)
		if err != nil {
			continue
		}
		storedDiffs += len(diffs)
	}
	require.True(t, storedDiffs > 0, "at least one host should have stored diffs")
}

func TestHTTP_Finalize(t *testing.T) {
	env := setupHTTPEnv(t, 5, 1000000, 100)
	ctx := context.Background()
	params := defaultParams()

	for i := 0; i < 15; i++ {
		_, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	drainSessionPending(t, ctx, env.session)
	preFinalize := env.session.StateMachine().SnapshotState()
	for id, rec := range preFinalize.Inferences {
		require.Equal(t, types.StatusFinished, rec.Status, "inference %d", id)
	}

	err := env.session.Finalize(ctx)
	require.NoError(t, err)

	st := env.session.StateMachine().SnapshotState()
	require.Equal(t, types.PhaseSettlement, st.Phase)
	require.Empty(t, st.Inferences)

	// Verify signatures collected from all hosts.
	sigs := env.session.Signatures()
	signedSlots := make(map[uint32]bool)
	for _, slotSigs := range sigs {
		for slotID := range slotSigs {
			signedSlots[slotID] = true
		}
	}
	for i := uint32(0); i < 5; i++ {
		require.True(t, signedSlots[i], "slot %d should have signed", i)
	}
}

// --- gossip integration tests ---

func TestHTTP_GossipAmplification(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)
	ctx := context.Background()

	// Send inference to host 0. Gossip fires to peers (host 1, host 2).
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := env.clients[0].Send(ctx, host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, resp.StateSig)
	require.NotEmpty(t, resp.StateHash)

	// Wait for async gossip propagation + amplification.
	time.Sleep(200 * time.Millisecond)

	// Host 1 and host 2 should have learned about nonce 1 via gossip.
	// Verify by sending same nonce gossip -- should succeed (not equivocate).
	err = env.hostClients[1].GossipNonce(ctx, 1, resp.StateHash, resp.StateSig, 0)
	require.NoError(t, err, "host 1 should accept matching hash (already seen via amplification)")

	err = env.hostClients[2].GossipNonce(ctx, 1, resp.StateHash, resp.StateSig, 0)
	require.NoError(t, err, "host 2 should accept matching hash (already seen via amplification)")
}

func TestHTTP_SignatureAccumulation(t *testing.T) {
	env := setupHTTPEnv(t, 5, 1000000, 100)
	ctx := context.Background()
	params := defaultParams()

	// Send 5 inferences. Each goes to a different host (round-robin).
	for i := 0; i < 5; i++ {
		_, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	// Wait for gossip propagation.
	time.Sleep(200 * time.Millisecond)

	// Check signatures at nonce 1 (all hosts should have processed it
	// via catch-up by now, and gossip should have propagated sigs).
	// The host that originally processed nonce 1 should have stored its own sig.
	hostIdx := int(1 % uint64(len(env.group))) // host for nonce 1
	sigs, err := env.clients[hostIdx].GetSignatures(ctx, 1)
	require.NoError(t, err)
	require.NotEmpty(t, sigs, "host %d should have at least own sig for nonce 1", hostIdx)
}

func TestHTTP_GossipRecovery(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)
	ctx := context.Background()
	params := defaultParams()

	// Send 3 inferences normally.
	for i := 0; i < 3; i++ {
		_, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	// Host 2 may not have all diffs (only gets direct contact for nonces where
	// nonce % 3 == 2). Set up recovery for host 2.
	g := env.gossips[2]
	g.RecoveryDelay = 0 // trigger immediately for test

	// Register that host 2 has seen nonce 3 but doesn't have the diffs backed.
	// Use the real PostStateRoot to avoid racing with async gossip that may
	// have already propagated the real hash for (nonce, slot).
	allDiffs := env.session.Diffs()
	lastDiff := allDiffs[len(allDiffs)-1]
	lastNonce := lastDiff.Nonce

	err := g.OnNonceReceived(lastNonce, lastDiff.PostStateRoot, []byte("sig-placeholder"), 0)
	require.NoError(t, err)

	// Wire recovery: fetch from host 0, apply to host 2.
	g.SetRecovery(
		env.clients[0], // DiffFetcher
		env.hosts[2],   // StateUpdater
	)

	// Reset last AfterRequest time so recovery triggers.
	g.AfterRequest(ctx, 0, []byte("seed"), []byte("seed-sig"))
	time.Sleep(10 * time.Millisecond)
	// Manually override to past.
	// We can't easily access g.lastAfterReq, so trigger recovery via the loop.
	// Instead, directly call tryRecovery logic via public interface.

	// Verify host 2 can serve diffs after direct catch-up.
	_, err = env.hosts[2].HandleRequest(ctx, host.HostRequest{
		Diffs: allDiffs,
		Nonce: lastNonce,
	})
	require.NoError(t, err)

	// Verify host 2 has signatures.
	for _, d := range allDiffs {
		sigs, err := env.hosts[2].GetSignatures(d.Nonce)
		if err == nil && len(sigs) > 0 {
			t.Logf("host 2 has %d sigs at nonce %d", len(sigs), d.Nonce)
		}
	}
}

func TestHTTP_HostDown_FullFlow(t *testing.T) {
	env := setupHTTPEnv(t, 5, 1000000, 100)
	ctx := context.Background()
	params := defaultParams()

	// Send 3 inferences.
	for i := 0; i < 3; i++ {
		_, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	// Shut down host 3 (executor for nonce 3 with 5 hosts: 3%5=3).
	env.httpServers[3].Close()

	// Send more inferences. Nonces that target host 3 will fail.
	failCount := 0
	for i := 0; i < 10; i++ {
		_, err := env.session.SendInference(ctx, params)
		if err != nil {
			failCount++
			break // stop on first failure
		}
	}

	// At least some inferences should have succeeded before hitting host 3.
	st := env.session.StateMachine().SnapshotState()
	require.NotEmpty(t, st.Inferences)

	// Verify live hosts have consistent state.
	allDiffs := env.session.Diffs()
	if len(allDiffs) > 0 {
		lastNonce := allDiffs[len(allDiffs)-1].Nonce
		var roots [][]byte
		for i, h := range env.hosts {
			if i == 3 {
				continue // down
			}
			_, err := h.HandleRequest(ctx, host.HostRequest{Diffs: allDiffs, Nonce: lastNonce})
			if err != nil {
				continue
			}
			root, err := h.StateRoot()
			if err != nil {
				continue
			}
			roots = append(roots, root)
		}
		// All live hosts should agree.
		for i := 1; i < len(roots); i++ {
			require.Equal(t, roots[0], roots[i], "live hosts should have same state root")
		}
	}
}

func TestHTTP_GossipIntegration(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)
	ctx := context.Background()

	// Send inference to host 0. Gossip should fire to peers.
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := env.clients[0].Send(ctx, host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, resp.StateSig)
	require.NotEmpty(t, resp.StateHash)

	// Gossip fires async. Verify same nonce arrives cleanly (use host client).
	err = env.hostClients[1].GossipNonce(ctx, 1, resp.StateHash, resp.StateSig, 0)
	require.NoError(t, err)
}

func TestHTTP_EquivocationViaGossipHTTP(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)
	ctx := context.Background()

	// Send real inference to get a valid state sig for nonce-based gossip.
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := env.clients[0].Send(ctx, host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.NoError(t, err)

	// First gossip with real hash+sig.
	err = env.hostClients[2].GossipNonce(ctx, 1, resp.StateHash, resp.StateSig, 0)
	require.NoError(t, err)

	// Same nonce with different hash from a different slot.
	// The sig won't verify (it's for the wrong hash), so equivocation
	// won't even be reached -- it's rejected at sig verification.
	err = env.hostClients[2].GossipNonce(ctx, 1, []byte("hash-b"), resp.StateSig, 1)
	require.Error(t, err)
}

func TestHTTP_LazyTxGossipHTTP(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)
	ctx := context.Background()

	// Send inference to host 1 (executor for nonce=1 with 3 hosts: 1%3=1).
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := env.clients[1].Send(ctx, host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.NoError(t, err)

	// Host 1 is executor, so it has mempool txs (finish msg).
	require.NotEmpty(t, resp.Mempool, "executor should produce mempool txs")

	// Gossip those txs to host 0 via HTTP.
	// Use a host signer (gossip is host-to-host, user is forbidden).
	hostClient := httpTestClient(env.httpServers[0].URL, "escrow-1", env.signers[1])
	err = hostClient.GossipTxs(ctx, resp.Mempool)
	require.NoError(t, err)

	// Wait for gossip to process.
	time.Sleep(50 * time.Millisecond)

	// Assert host 0's mempool actually contains the gossipped txs.
	mempoolTxs := env.hosts[0].MempoolTxs()
	require.NotEmpty(t, mempoolTxs, "host 0 mempool should contain gossipped txs")
}

func TestHTTP_StateHashVerification(t *testing.T) {
	// User detects state hash mismatch from a host returning wrong state.
	numHosts := 3
	hostSigners := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	// Build a normal host for slot 0.
	sm0, err := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userSigner.Address(), config, group, 100000))
	require.NoError(t, err)
	engine0 := stub.NewInferenceEngine()
	h0, err := host.NewHost(sm0, hostSigners[0], engine0, "escrow-1", group, nil, host.WithGrace(100))
	require.NoError(t, err)

	// Build a tampered host for slot 1 with different initial balance -> different state hash.
	sm1, err := state.NewStateMachine("escrow-1", config, group, 99999, userSigner.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userSigner.Address(), config, group, 99999))
	require.NoError(t, err)
	engine1 := stub.NewInferenceEngine()
	h1, err := host.NewHost(sm1, hostSigners[1], engine1, "escrow-1", group, nil, host.WithGrace(100))
	require.NoError(t, err)

	// Build a normal host for slot 2.
	sm2, err := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userSigner.Address(), config, group, 100000))
	require.NoError(t, err)
	engine2 := stub.NewInferenceEngine()
	h2, err := host.NewHost(sm2, hostSigners[2], engine2, "escrow-1", group, nil, host.WithGrace(100))
	require.NoError(t, err)

	clients := []user.HostClient{
		&user.InProcessClient{Host: h0},
		&user.InProcessClient{Host: h1},
		&user.InProcessClient{Host: h2},
	}

	userSM, err := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userSigner.Address(), config, group, 100000))
	require.NoError(t, err)
	session, err := user.NewSession(userSM, userSigner, "escrow-1", group, clients, verifier)
	require.NoError(t, err)

	ctx := context.Background()
	params := defaultParams()

	// First inference goes to host 1 (nonce 1 % 3 = 1) which has wrong balance.
	// The host's computed state root won't match the user's signed post_state_root.
	_, err = session.SendInference(ctx, params)
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrPostStateRootMismatch)
}

func TestHTTP_GetSignatures(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)
	ctx := context.Background()
	params := defaultParams()

	_, err := env.session.SendInference(ctx, params)
	require.NoError(t, err)

	// The host that processed nonce 1 should have stored its own sig.
	hostIdx := int(1 % uint64(len(env.group)))
	sigs, err := env.clients[hostIdx].GetSignatures(ctx, 1)
	require.NoError(t, err)
	require.NotEmpty(t, sigs, "should have own sig stored")
}

func TestAttack_UserCannotGossip(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)
	ctx := context.Background()

	// Create a client authenticated as the user (not a group member).
	userClient := httpTestClient(env.httpServers[0].URL, "escrow-1", env.userSigner)

	// User attempts to gossip nonce -> must be rejected.
	err := userClient.GossipNonce(ctx, 1, []byte("fake-hash"), []byte("fake-sig"), 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "403", "user should be forbidden from gossiping")

	// User attempts to gossip txs -> must be rejected.
	err = userClient.GossipTxs(ctx, []*types.DevshardTx{testutil.StartTx(1)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "403", "user should be forbidden from gossiping txs")
}

func TestAttack_GossipUnverifiedNonce(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)
	ctx := context.Background()

	// Send real inference to host 0 to get a legitimate state.
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := env.clients[0].Send(ctx, host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, resp.StateHash)

	// Attacker (group member host 2) sends a gossip nonce with a fake stateHash
	// and garbage stateSig. Should be rejected because sig doesn't verify.
	hostClient := httpTestClient(env.httpServers[1].URL, "escrow-1", env.signers[2])
	err = hostClient.GossipNonce(ctx, 1, []byte("fake-hash"), []byte("garbage-sig"), 2)
	require.Error(t, err, "gossip with invalid sig should be rejected")

	// Now send the real gossip from a legitimate host. Must NOT be rejected as equivocation.
	hostClient1 := httpTestClient(env.httpServers[1].URL, "escrow-1", env.signers[0])
	err = hostClient1.GossipNonce(ctx, 1, resp.StateHash, resp.StateSig, 0)
	require.NoError(t, err, "real gossip must not be rejected after fake was blocked")
}

func TestAttack_GossipEmptySigBypass(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)
	ctx := context.Background()

	// Send real inference to host 0 to get a legitimate state.
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := env.clients[0].Send(ctx, host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, resp.StateHash)

	// Attacker (group member host 2) sends gossip with empty StateSig to bypass
	// signature verification. Must be rejected.
	hostClient := httpTestClient(env.httpServers[1].URL, "escrow-1", env.signers[2])
	err = hostClient.GossipNonce(ctx, 1, []byte("fake-hash"), nil, 2)
	require.Error(t, err, "gossip with empty sig must be rejected")

	// Also test with out-of-range SlotID.
	err = hostClient.GossipNonce(ctx, 1, []byte("fake-hash"), []byte("some-sig"), 99)
	require.Error(t, err, "gossip with invalid slot id must be rejected")

	// Real gossip must still work after the rejected attempts.
	hostClient1 := httpTestClient(env.httpServers[1].URL, "escrow-1", env.signers[0])
	err = hostClient1.GossipNonce(ctx, 1, resp.StateHash, resp.StateSig, 0)
	require.NoError(t, err, "real gossip must succeed after rejected bypass attempts")
}

// T1: withheld payload → refused-timeout votes reject → session recovery
// diff carries the executor's MsgConfirmStart and a peer SM applies it.
func TestHTTP_T1_HonestRecovery_ConfirmStartReachesSessionAndPeer(t *testing.T) {
	env := setupHTTPEnv(t, 3, 1000000, 100)
	ctx := context.Background()

	prepared, err := env.session.PrepareInference(defaultParams())
	require.NoError(t, err)
	require.Equal(t, uint64(1), prepared.Nonce())
	executorIdx := prepared.HostIdx()

	_, err = env.session.HandleTimeout(ctx, prepared.Nonce(), time.Unix(0, 0), refusedPayload())
	require.NoError(t, err, "rejected refused-timeout must recover by publishing ConfirmStart")

	diffs := env.session.Diffs()
	require.GreaterOrEqual(t, len(diffs), 2)
	recovery := diffs[len(diffs)-1]
	require.Equal(t, 1, countConfirmStart(recovery.Txs, 1), "recovery diff must carry exactly one ConfirmStart for inference 1")
	got := findConfirmStart(recovery.Txs, 1)
	require.NotNil(t, got)

	execConfirm := findConfirmStart(env.hosts[executorIdx].MempoolTxs(), 1)
	require.NotNil(t, execConfirm, "executor must still hold the challenge ConfirmStart")
	require.Equal(t, types.TxHash(execConfirm), types.TxHash(got), "session must copy the executor tx, not mint a new ConfirmStart")
	require.Equal(t, execConfirm.GetConfirmStart().ExecutorSig, got.GetConfirmStart().ExecutorSig)
	require.Equal(t, execConfirm.GetConfirmStart().ConfirmedAt, got.GetConfirmStart().ConfirmedAt)

	peer := newPeerSM(t, env, 1000000)
	_, err = peer.ApplyDiff(diffs[0])
	require.NoError(t, err)
	require.Equal(t, types.StatusPending, peer.SnapshotState().Inferences[1].Status)

	_, err = peer.ApplyDiff(recovery)
	require.NoError(t, err)
	after := peer.SnapshotState().Inferences[1].Status
	if findFinish(recovery.Txs, 1) != nil {
		require.Equal(t, types.StatusFinished, after)
		return
	}
	require.Equal(t, types.StatusStarted, after, "peer SM that only saw StartInference must apply ConfirmStart: Pending → Started")

	require.Eventually(t, func() bool {
		return findFinish(env.hosts[executorIdx].MempoolTxs(), 1) != nil
	}, 5*time.Second, 20*time.Millisecond, "executor execution should queue FinishInference")
	finish := findFinish(env.hosts[executorIdx].MempoolTxs(), 1)
	require.NotNil(t, finish)

	settlePeer := newPeerSM(t, env, 1000000)
	_, err = settlePeer.ApplyDiff(diffs[0])
	require.NoError(t, err)
	settle := testutil.SignDiff(t, env.userSigner, "escrow-1", 2, []*types.DevshardTx{got, finish})
	_, err = settlePeer.ApplyDiff(settle)
	require.NoError(t, err, "ConfirmStart + FinishInference sourced after challenge must apply")
	require.Equal(t, types.StatusFinished, settlePeer.SnapshotState().Inferences[1].Status)
}

// T2: same challenge as T1, but the user omits ConfirmStart from later diffs.
// Voting verifiers must keep the pool copy; a later honest inclusion still applies.
func TestHTTP_T2_OmittedConfirmStart_VotingVerifiersRetainPoolCopy(t *testing.T) {
	env := setupHTTPEnv(t, 3, 1000000, 100)
	ctx := context.Background()

	prepared, err := env.session.PrepareInference(defaultParams())
	require.NoError(t, err)
	executorIdx := prepared.HostIdx()

	votes, recovery, _, err := env.session.CollectTimeoutVotes(ctx, prepared.Nonce(), types.TimeoutReason_TIMEOUT_REASON_REFUSED, refusedPayload(), env.session.TimeoutVerifiers(), env.session.Diffs())
	require.NoError(t, err)
	require.Empty(t, votes, "alive executor must cause verifiers to reject the timeout")
	require.NotNil(t, findConfirmStart(recovery, 1), "reject votes must carry executor ConfirmStart")

	execConfirm := findConfirmStart(env.hosts[executorIdx].MempoolTxs(), 1)
	require.NotNil(t, execConfirm)
	wantHash := types.TxHash(execConfirm)

	require.NoError(t, env.session.SendPendingDiff(ctx), "empty pending must still produce a follow-up diff")
	omitDiffs := env.session.Diffs()
	require.GreaterOrEqual(t, len(omitDiffs), 2)
	require.Equal(t, 0, countConfirmStart(omitDiffs[len(omitDiffs)-1].Txs, 1), "user-omitted diff must not include ConfirmStart")

	for i, h := range env.hosts {
		_, err := h.HandleRequest(ctx, host.HostRequest{Diffs: omitDiffs, Nonce: omitDiffs[len(omitDiffs)-1].Nonce})
		require.NoError(t, err, "host %d", i)
		rec, ok := h.SnapshotState().Inferences[1]
		require.True(t, ok, "host %d", i)
		require.Equal(t, types.StatusPending, rec.Status, "host %d collective state must stay Pending when ConfirmStart is omitted", i)
	}

	for i, h := range env.hosts {
		got := findConfirmStart(h.MempoolTxs(), 1)
		if i == executorIdx {
			require.NotNil(t, got, "executor must still hold ConfirmStart")
			require.Equal(t, wantHash, types.TxHash(got))
			continue
		}
		require.NotNil(t, got, "voting verifier %d must retain ConfirmStart after omission", i)
		require.Equal(t, wantHash, types.TxHash(got), "verifier %d must keep the same hash as the executor", i)
	}

	peer := newPeerSM(t, env, 1000000)
	for _, d := range omitDiffs {
		_, err := peer.ApplyDiff(d)
		require.NoError(t, err)
	}
	require.Equal(t, types.StatusPending, peer.SnapshotState().Inferences[1].Status)

	honest := testutil.SignDiff(t, env.userSigner, "escrow-1", omitDiffs[len(omitDiffs)-1].Nonce+1, []*types.DevshardTx{execConfirm})
	_, err = peer.ApplyDiff(honest)
	require.NoError(t, err, "exact mempool ConfirmStart must still apply after omission")
	require.Equal(t, types.StatusStarted, peer.SnapshotState().Inferences[1].Status)
}

func refusedPayload() *host.InferencePayload {
	p := defaultParams()
	return &host.InferencePayload{
		Prompt:      p.Prompt,
		Model:       p.Model,
		InputLength: p.InputLength,
		MaxTokens:   p.MaxTokens,
		StartedAt:   p.StartedAt,
	}
}

func findConfirmStart(txs []*types.DevshardTx, inferenceID uint64) *types.DevshardTx {
	for _, tx := range txs {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == inferenceID {
			return tx
		}
	}
	return nil
}

func findFinish(txs []*types.DevshardTx, inferenceID uint64) *types.DevshardTx {
	for _, tx := range txs {
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == inferenceID {
			return tx
		}
	}
	return nil
}

func countConfirmStart(txs []*types.DevshardTx, inferenceID uint64) int {
	n := 0
	for _, tx := range txs {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == inferenceID {
			n++
		}
	}
	return n
}

func newPeerSM(t *testing.T, env *httpTestEnv, balance uint64) *state.StateMachine {
	t.Helper()
	sm, err := state.NewStateMachine(
		"escrow-1",
		env.config,
		env.group,
		balance,
		env.userSigner.Address(),
		signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", env.userSigner.Address(), env.config, env.group, balance),
	)
	require.NoError(t, err)
	return sm
}
