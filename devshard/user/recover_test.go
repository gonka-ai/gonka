package user

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

func newTestStateMachine(
	t *testing.T,
	escrowID string,
	config types.SessionConfig,
	group []types.SlotAssignment,
	balance uint64,
	userAddr string,
	verifier signing.Verifier,
	opts ...state.SMOption,
) *state.StateMachine {
	t.Helper()
	opts = append([]state.SMOption{state.WithStateRootAndProtocolVersion(testutil.RuntimeTestVersion)}, opts...)
	sm, err := state.NewStateMachine(escrowID, config, group, balance, userAddr, verifier, testutil.MustMemoryStore(t, escrowID, userAddr, config, group, balance), opts...)
	require.NoError(t, err)
	return sm
}

func newTestStore(t *testing.T) *storage.SQLite {
	t.Helper()
	db, err := storage.NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

// setupRecoverableSession creates a session with SQLite storage and sends
// numInferences inferences. Returns the store, group, hosts, user signer,
// and the final nonce reached.
func setupRecoverableSession(
	t *testing.T, numHosts int, numInferences int, store storage.Storage,
) ([]types.SlotAssignment, []*signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	// Create storage session.
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	// Create hosts.
	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		engine := stub.NewInferenceEngine()
		h, err := host.NewHost(sm, hosts[i], engine, "escrow-1", group, nil, host.WithGrace(10))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	// Create user session with storage.
	userSM := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier, WithStorage(store))
	require.NoError(t, err)

	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	for i := 0; i < numInferences; i++ {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	return group, hosts, user
}

func TestRecoverSession_HappyPath(t *testing.T) {
	store := newTestStore(t)
	numHosts := 3
	numInferences := 5

	group, hosts, user := setupRecoverableSession(t, numHosts, numInferences, store)

	// Build fresh clients for recovery.
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		engine := stub.NewInferenceEngine()
		h, err := host.NewHost(sm, hosts[i], engine, "escrow-1", group, nil, host.WithGrace(10))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	// Recover.
	session, _, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group, clients)
	require.NoError(t, err)
	require.Equal(t, uint64(numInferences), session.Nonce())
	require.Len(t, session.Diffs(), numInferences)

	// Verify can send nonce 6.
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	resp, err := session.SendInference(ctx, params)
	require.NoError(t, err)
	require.Equal(t, uint64(numInferences+1), resp.Nonce)
}

// Production reproduction: a node that had served sub-floor reservations could not restart once the
// floor landed, because replaying its own persisted diffs re-ran a rule written after they were made.
func TestRecoverSession_ReplaysADiffWrittenBeforeTheMaxTokensFloor(t *testing.T) {
	store := newTestStore(t)
	numHosts := 3
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	subFloor := []*types.DevshardTx{{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens - 1, StartedAt: 1000,
	}}}}

	writerSM := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	_, err := writerSM.ApplyLocal(1, subFloor)
	require.ErrorIs(t, err, types.ErrMaxTokensBelowFloor, "the fixture must be a diff this build refuses to author")
	root, err := writerSM.ApplyLocalPersisted(1, subFloor)
	require.NoError(t, err)
	require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{
		Diff:      testutil.SignDiffWithRoot(t, user, "escrow-1", 1, subFloor, root),
		StateHash: root,
	}))

	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(10))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	session, _, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group, clients)
	require.NoError(t, err)
	require.Equal(t, uint64(1), session.Nonce())
	require.Len(t, session.Diffs(), 1)
}

func TestRecoverSession_EmptySession(t *testing.T) {
	store := newTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(3)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	clients := make([]HostClient, 3)
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil)
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	session, _, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group, clients)
	require.NoError(t, err)
	require.Equal(t, uint64(0), session.Nonce())
}

func setupRecoverableHeartbeatSession(
	t *testing.T,
	store storage.Storage,
	height *uint64,
	now *time.Time,
) (*Session, []types.SlotAssignment, []*signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	const numHosts = 3
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	userSM := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier,
		WithStorage(store),
		WithHeightSyncCadence(10, uint64(numHosts)),
		WithObservedHeight(func() (uint64, []byte, bool) {
			h := *height
			if h == 0 {
				return 0, nil, false
			}
			return h, []byte{0xaa}, true
		}),
		WithHeartbeatClock(func() time.Time { return *now }),
	)
	require.NoError(t, err)
	return session, group, hosts, user
}

func setupRecoverableSeedSession(
	t *testing.T,
	store storage.Storage,
	height uint64,
) (*Session, []types.SlotAssignment, []*signing.Secp256k1Signer, *signing.Secp256k1Signer, []*seededInProcessClient) {
	t.Helper()
	const numHosts = 3
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	seedClients := make([]*seededInProcessClient, numHosts)
	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		seedClients[i] = &seededInProcessClient{
			InProcessClient: &InProcessClient{Host: h},
			height:          height,
			hash:            []byte{0xaa, byte(i + 1)},
		}
		clients[i] = seedClients[i]
	}

	userSM := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier,
		WithStorage(store),
		WithHeightSyncCadence(10, uint64(numHosts)),
	)
	require.NoError(t, err)
	return session, group, hosts, user, seedClients
}

func setupRecoverableHeartbeatSessionWithOracles(
	t *testing.T,
	store storage.Storage,
	height *uint64,
	now *time.Time,
	oracles []*sessionOracle,
) (*Session, []types.SlotAssignment, []*signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	const numHosts = 3
	require.Len(t, oracles, numHosts)
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil,
			host.WithGrace(100),
			host.WithChainOracle(oracles[i]),
		)
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	userSM := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier,
		WithStorage(store),
		WithHeightSyncCadence(10, uint64(numHosts)),
		WithObservedHeight(func() (uint64, []byte, bool) {
			h := *height
			if h == 0 {
				return 0, nil, false
			}
			return h, []byte{0xaa}, true
		}),
		WithHeartbeatClock(func() time.Time { return *now }),
	)
	require.NoError(t, err)
	return session, group, hosts, user
}

func recoverHeartbeatSession(
	t *testing.T,
	store storage.Storage,
	group []types.SlotAssignment,
	hosts []*signing.Secp256k1Signer,
	user *signing.Secp256k1Signer,
	height *uint64,
) *Session {
	t.Helper()
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	clients := make([]HostClient, len(hosts))
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}
	session, _, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group, clients)
	require.NoError(t, err)
	session.SetHeightSyncCadence(10, uint64(len(hosts)))
	session.SetObservedHeight(func() (uint64, []byte, bool) {
		h := *height
		if h == 0 {
			return 0, nil, false
		}
		return h, []byte{0xaa}, true
	})
	return session
}

func heartbeatTxForTurn(diffs []types.Diff, turnSeq uint64) *types.MsgHeartbeat {
	for _, d := range diffs {
		for _, tx := range d.Txs {
			if inner := tx.GetHeartbeat(); inner != nil && inner.TurnSeq == turnSeq {
				return inner
			}
		}
	}
	return nil
}

func TestRecoverSession_HeartbeatContinuesTurnSeq(t *testing.T) {
	store := newTestStore(t)
	var height uint64 = 100
	now := time.Unix(1000, 0).UTC()
	session, group, hosts, user := setupRecoverableHeartbeatSession(t, store, &height, &now)
	ctx := context.Background()
	interval := heightsync.DefaultHeartbeatConfig().Interval

	for turn := uint64(1); turn <= 3; turn++ {
		require.NoError(t, session.MaybeHeartbeat(ctx), "turn %d", turn)
		rec := session.HeartbeatTurnTracker().Record(turn)
		require.NotNil(t, rec, "turn %d", turn)
		require.Equal(t, heightsync.TurnComplete, rec.State, "turn %d", turn)
		if turn < 3 {
			now = now.Add(interval + time.Second)
		}
	}
	require.Equal(t, uint64(3), session.StateMachine().HeightSyncLatestTurnSeq())
	require.NoError(t, session.FlushSnapshot())
	require.NoError(t, session.Close())

	recovered := recoverHeartbeatSession(t, store, group, hosts, user, &height)
	t.Cleanup(func() { _ = recovered.Close() })

	require.Equal(t, uint64(3), recovered.StateMachine().HeightSyncLatestTurnSeq())
	prev := recovered.HeartbeatTurnTracker().Record(3)
	require.NotNil(t, prev, "recovered producer must hold turn 3 before composing turn 4")
	require.Equal(t, heightsync.TurnComplete, prev.State)

	require.NoError(t, recovered.MaybeHeartbeat(ctx))
	hb := heartbeatTxForTurn(recovered.Diffs(), 4)
	require.NotNil(t, hb, "next heartbeat after recover must be turn_seq=4")
	require.Len(t, hb.SyncVector, 3)
	for i, ent := range hb.SyncVector {
		require.Equal(t, types.AckStatus_ACKED, ent.Status, "slot %d", i)
	}
}

func TestRecoverSession_HeartbeatPendingAckLossDoesNotDuplicateTurnOrStall(t *testing.T) {
	store := newTestStore(t)
	var height uint64 = 100
	now := time.Unix(1000, 0).UTC()
	session, group, hosts, user := setupRecoverableHeartbeatSession(t, store, &height, &now)
	ctx := context.Background()

	span, err := session.composeHeartbeatSpan()
	require.NoError(t, err)
	require.Len(t, span, 3, "sanity: the heartbeat span should address every slot")
	session.dispatchHeartbeatSpan(ctx, span)
	require.Len(t, heightAcksInTxs(session.PendingTxs()), 3,
		"host acks exist only in the recovered-away pending mempool before flush")
	require.Equal(t, uint64(3), session.Nonce())
	require.Equal(t, uint64(1), session.StateMachine().HeightSyncLatestTurnSeq())
	require.Equal(t, heightsync.TurnOpen, session.HeartbeatTurnTracker().Record(1).State)
	require.NoError(t, session.Close())

	recovered, _, err := RecoverSession(store, user, signing.NewSecp256k1Verifier(),
		"escrow-1", testutil.RuntimeTestVersion, group,
		recoveryHeartbeatClients(t, group, hosts, user))
	require.NoError(t, err)
	t.Cleanup(func() { _ = recovered.Close() })
	recovered.SetHeightSyncCadence(10, uint64(len(hosts)))
	recovered.SetObservedHeight(func() (uint64, []byte, bool) { return height, []byte{0xaa}, true })
	recoveredAt := time.Now()
	recovered.clock = func() time.Time { return recoveredAt }

	require.Empty(t, recovered.PendingTxs(), "pending height acks are process-local and disappear on restart")
	require.Equal(t, uint64(3), recovered.Nonce())
	require.Equal(t, uint64(1), recovered.StateMachine().HeightSyncLatestTurnSeq())
	require.Equal(t, heightsync.TurnOpen, recovered.HeartbeatTurnTracker().Record(1).State)

	require.NoError(t, recovered.MaybeHeartbeat(ctx))
	require.Equal(t, uint64(3), recovered.Nonce(),
		"the recovered in-flight turn must suppress an immediate duplicate turn")
	require.Len(t, heartbeatsForTurn(recovered.Diffs(), 1), 3)
	require.Empty(t, heartbeatsForTurn(recovered.Diffs(), 2))

	height = 101
	recoveredAt = recoveredAt.Add(recovered.heartbeat.Config().TurnTimeout + time.Second)
	require.NoError(t, recovered.MaybeHeartbeat(ctx))
	require.GreaterOrEqual(t, recovered.Nonce(), uint64(6),
		"lost pre-flush acks must not permanently stall the heartbeat producer")
	require.Len(t, heartbeatsForTurn(recovered.Diffs(), 1), 3,
		"recovery must not replay or duplicate the abandoned turn_seq=1 span")
	require.Len(t, heartbeatsForTurn(recovered.Diffs(), 2), 3,
		"after TurnTimeout the producer may abandon the lost-ack turn and open turn_seq=2")
	require.NotNil(t, recovered.HeartbeatTurnTracker().Record(2))
	require.Equal(t, uint64(2), recovered.StateMachine().HeightSyncLatestTurnSeq())
}

func TestRecoverSession_HeartbeatPartialPendingAckLossDoesNotStall(t *testing.T) {
	store := newTestStore(t)
	var height uint64 = 100
	now := time.Unix(1000, 0).UTC()
	session, group, hosts, user := setupRecoverableHeartbeatSession(t, store, &height, &now)
	ctx := context.Background()

	span, err := session.composeHeartbeatSpan()
	require.NoError(t, err)
	require.Len(t, span, 3)
	session.dispatchHeartbeatSpan(ctx, span[:1])
	require.Len(t, heightAcksInTxs(session.PendingTxs()), 1,
		"only one volatile ack made it back before restart")
	require.NoError(t, session.Close())

	recovered, _, err := RecoverSession(store, user, signing.NewSecp256k1Verifier(),
		"escrow-1", testutil.RuntimeTestVersion, group,
		recoveryHeartbeatClients(t, group, hosts, user))
	require.NoError(t, err)
	t.Cleanup(func() { _ = recovered.Close() })
	recovered.SetHeightSyncCadence(10, uint64(len(hosts)))
	recovered.SetObservedHeight(func() (uint64, []byte, bool) { return height, []byte{0xaa}, true })
	recoveredAt := time.Now()
	recovered.clock = func() time.Time { return recoveredAt }

	require.Empty(t, recovered.PendingTxs())
	require.Equal(t, uint64(3), recovered.Nonce())
	require.Equal(t, uint64(1), recovered.StateMachine().HeightSyncLatestTurnSeq())
	require.Equal(t, heightsync.TurnOpen, recovered.HeartbeatTurnTracker().Record(1).State)
	require.NoError(t, recovered.MaybeHeartbeat(ctx))
	require.Equal(t, uint64(3), recovered.Nonce(), "open turn suppresses immediate duplicate span")
	require.Empty(t, heartbeatsForTurn(recovered.Diffs(), 2))

	height = 101
	recoveredAt = recoveredAt.Add(recovered.heartbeat.Config().TurnTimeout + time.Second)
	require.NoError(t, recovered.MaybeHeartbeat(ctx))
	require.Len(t, heartbeatsForTurn(recovered.Diffs(), 1), 3)
	require.Len(t, heartbeatsForTurn(recovered.Diffs(), 2), 3)
	require.Equal(t, uint64(2), recovered.StateMachine().HeightSyncLatestTurnSeq())
}

func TestRecoverSession_HeartbeatPartialAckDurableLossReportsSyncVector(t *testing.T) {
	store := newTestStore(t)
	var height uint64 = 100
	now := time.Unix(1000, 0).UTC()
	session, group, hosts, user := setupRecoverableHeartbeatSession(t, store, &height, &now)
	ctx := context.Background()

	span, err := session.composeHeartbeatSpan()
	require.NoError(t, err)
	require.Len(t, span, 3)
	session.dispatchHeartbeatSpan(ctx, span[:1])
	require.Len(t, heightAcksInTxs(session.PendingTxs()), 1)
	session.mu.Lock()
	ackDiff, _, err := session.composeDiffLocked(nil)
	session.mu.Unlock()
	require.NoError(t, err)
	require.Len(t, heightAcksInDiffs([]types.Diff{ackDiff}), 1,
		"one ack reached durable diff before the remaining volatile acks were lost")
	rec := session.HeartbeatTurnTracker().Record(1)
	require.NotNil(t, rec)
	require.Equal(t, heightsync.TurnOpen, rec.State)
	require.NoError(t, session.Close())

	recovered, _, err := RecoverSession(store, user, signing.NewSecp256k1Verifier(),
		"escrow-1", testutil.RuntimeTestVersion, group,
		recoveryHeartbeatClients(t, group, hosts, user))
	require.NoError(t, err)
	t.Cleanup(func() { _ = recovered.Close() })
	recovered.SetHeightSyncCadence(10, uint64(len(hosts)))
	recovered.SetObservedHeight(func() (uint64, []byte, bool) { return height, []byte{0xaa}, true })
	recoveredAt := time.Now().Add(recovered.heartbeat.Config().TurnTimeout + time.Second)
	recovered.clock = func() time.Time { return recoveredAt }

	require.Equal(t, uint64(4), recovered.Nonce())
	rec = recovered.HeartbeatTurnTracker().Record(1)
	require.NotNil(t, rec)
	require.Equal(t, heightsync.TurnOpen, rec.State)
	require.Len(t, rec.Acks, 1)

	height = 101
	require.NoError(t, recovered.MaybeHeartbeat(ctx))
	hb := heartbeatTxForTurn(recovered.Diffs(), 2)
	require.NotNil(t, hb)
	require.Len(t, hb.SyncVector, 3)
	statuses := syncVectorStatuses(hb.SyncVector)
	require.Equal(t, types.AckStatus_ACKED, statuses[span[0].hostIdx])
	for slot := 0; slot < len(hosts); slot++ {
		if slot == span[0].hostIdx {
			continue
		}
		require.Equal(t, types.AckStatus_MISSING, statuses[slot], "slot %d", slot)
	}
}

func TestRecoverSession_HeartbeatLateOldAckDoesNotCreditNewTurn(t *testing.T) {
	store := newTestStore(t)
	var height uint64 = 100
	now := time.Unix(1000, 0).UTC()
	session, group, hosts, user := setupRecoverableHeartbeatSession(t, store, &height, &now)
	ctx := context.Background()

	span, err := session.composeHeartbeatSpan()
	require.NoError(t, err)
	require.Len(t, span, 3)
	session.dispatchHeartbeatSpan(ctx, span[:1])
	oldAck := heightAcksInTxs(session.PendingTxs())[0]
	require.NoError(t, session.Close())

	recovered, _, err := RecoverSession(store, user, signing.NewSecp256k1Verifier(),
		"escrow-1", testutil.RuntimeTestVersion, group,
		recoveryHeartbeatClients(t, group, hosts, user))
	require.NoError(t, err)
	t.Cleanup(func() { _ = recovered.Close() })
	recovered.SetHeightSyncCadence(10, uint64(len(hosts)))
	recovered.SetObservedHeight(func() (uint64, []byte, bool) { return height, []byte{0xaa}, true })
	recoveredAt := time.Now().Add(recovered.heartbeat.Config().TurnTimeout + time.Second)
	recovered.clock = func() time.Time { return recoveredAt }

	height = 101
	require.NoError(t, recovered.MaybeHeartbeat(ctx))
	rec2Before := recovered.HeartbeatTurnTracker().Record(2)
	require.NotNil(t, rec2Before)
	require.NotEmpty(t, rec2Before.Acks)
	rec2AcksBefore := ackNoncesBySlot(rec2Before)
	nonceAfterTurn2 := recovered.Nonce()
	rootBefore, err := recovered.StateMachine().ComputeStateRoot()
	require.NoError(t, err)
	floorBefore, floorHashBefore, floorKnownBefore := recovered.StateMachine().HeightSyncFloorAsOf(nonceAfterTurn2 + 1)

	require.NoError(t, recovered.ProcessResponse(span[0].hostIdx, &host.HostResponse{
		Nonce:   recovered.Nonce(),
		Mempool: []*types.DevshardTx{{Tx: &types.DevshardTx_HeightAck{HeightAck: oldAck}}},
	}, recovered.Nonce()))
	require.Empty(t, heightAcksInTxs(recovered.PendingTxs()),
		"the late old ack was already recovered through catch-up and must not be queued again")
	require.Equal(t, nonceAfterTurn2, recovered.Nonce())
	rec2After := recovered.HeartbeatTurnTracker().Record(2)
	require.Equal(t, rec2AcksBefore, ackNoncesBySlot(rec2After),
		"a late turn_seq=1 ack must not count as an ack for turn_seq=2")
	rootAfter, err := recovered.StateMachine().ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, rootBefore, rootAfter, "duplicate old ack must not change state root")
	floorAfter, floorHashAfter, floorKnownAfter := recovered.StateMachine().HeightSyncFloorAsOf(nonceAfterTurn2 + 1)
	require.Equal(t, floorKnownBefore, floorKnownAfter)
	require.Equal(t, floorBefore, floorAfter)
	require.Equal(t, floorHashBefore, floorHashAfter)
}

func TestRecoverSession_HeartbeatAckFlushPersistedBeforeHostCatchup(t *testing.T) {
	store := newTestStore(t)
	var height uint64 = 100
	now := time.Unix(1000, 0).UTC()
	session, group, hosts, user := setupRecoverableHeartbeatSession(t, store, &height, &now)
	ctx := context.Background()

	span, err := session.composeHeartbeatSpan()
	require.NoError(t, err)
	require.Len(t, span, 3)
	session.dispatchHeartbeatSpan(ctx, span)
	require.Len(t, heightAcksInTxs(session.PendingTxs()), 3)
	session.mu.Lock()
	ackDiff, ackHostIdx, err := session.composeDiffLocked(nil)
	session.mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, uint64(4), ackDiff.Nonce)
	require.Len(t, heightAcksInDiffs([]types.Diff{ackDiff}), 3)
	require.Equal(t, heightsync.TurnComplete, session.HeartbeatTurnTracker().Record(1).State)
	require.Less(t, session.hostSyncNonce[ackHostIdx], ackDiff.Nonce,
		"the ack-flush diff is durable before the target host receives it")
	require.NoError(t, session.Close())

	recoveryClients := recoveryHeartbeatClients(t, group, hosts, user)
	targetHost := recoveryClients[ackHostIdx].(*InProcessClient).Host
	require.Equal(t, uint64(0), targetHost.LatestNonce(),
		"fresh recovered host has not received the persisted ack-flush diff yet")

	recovered, _, err := RecoverSession(store, user, signing.NewSecp256k1Verifier(),
		"escrow-1", testutil.RuntimeTestVersion, group,
		recoveryClients)
	require.NoError(t, err)
	t.Cleanup(func() { _ = recovered.Close() })
	recovered.SetHeightSyncCadence(10, uint64(len(hosts)))
	recovered.SetObservedHeight(func() (uint64, []byte, bool) { return height, []byte{0xaa}, true })

	require.Equal(t, uint64(4), recovered.Nonce())
	require.Less(t, recovered.hostSyncNonce[ackHostIdx], ackDiff.Nonce,
		"recovered cursor may still be behind the latest durable nonce")
	require.Equal(t, heightsync.TurnComplete, recovered.HeartbeatTurnTracker().Record(1).State)
	rootBefore, err := recovered.StateMachine().ComputeStateRoot()
	require.NoError(t, err)
	diffCountBefore := len(recovered.Diffs())
	require.NoError(t, recovered.sendCatchUp(ctx, ackHostIdx))
	require.Equal(t, ackDiff.Nonce, recovered.hostSyncNonce[ackHostIdx])
	require.Equal(t, ackDiff.Nonce, targetHost.LatestNonce(),
		"catch-up must apply the persisted ack-flush diff to the lagging host")
	require.Equal(t, uint64(4), recovered.Nonce(), "catch-up must not compose a duplicate ack-flush diff")
	require.Len(t, recovered.Diffs(), diffCountBefore, "catch-up replays existing diffs only")
	rootAfter, err := recovered.StateMachine().ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, rootBefore, rootAfter, "catch-up must not mutate sequencer state")
	require.Empty(t, recovered.PendingTxs())
}

func TestRecoverSession_HeartbeatNoHeightWhileOpenDoesNotDuplicateThenRecovers(t *testing.T) {
	store := newTestStore(t)
	var height uint64 = 100
	now := time.Unix(1000, 0).UTC()
	session, group, hosts, user := setupRecoverableHeartbeatSession(t, store, &height, &now)
	ctx := context.Background()

	span, err := session.composeHeartbeatSpan()
	require.NoError(t, err)
	require.Len(t, span, 3)
	session.dispatchHeartbeatSpan(ctx, span[:1])
	require.Len(t, heightAcksInTxs(session.PendingTxs()), 1)
	require.NoError(t, session.Close())

	recovered, _, err := RecoverSession(store, user, signing.NewSecp256k1Verifier(),
		"escrow-1", testutil.RuntimeTestVersion, group,
		recoveryHeartbeatClients(t, group, hosts, user))
	require.NoError(t, err)
	t.Cleanup(func() { _ = recovered.Close() })
	recovered.SetHeightSyncCadence(10, uint64(len(hosts)))
	var recoveredHeight uint64
	recovered.SetObservedHeight(func() (uint64, []byte, bool) {
		if recoveredHeight == 0 {
			return 0, nil, false
		}
		return recoveredHeight, []byte{0xaa}, true
	})
	recoveredAt := time.Now().Add(recovered.heartbeat.Config().TurnTimeout + time.Second)
	recovered.clock = func() time.Time { return recoveredAt }

	require.NoError(t, recovered.MaybeHeartbeat(ctx))
	require.Equal(t, uint64(3), recovered.Nonce(),
		"without an observed height recovery must not invent a duplicate turn")
	require.Empty(t, heartbeatsForTurn(recovered.Diffs(), 2))
	require.Equal(t, 1, recovered.HeartbeatSkippedNoHeight())

	recoveredHeight = 101
	require.NoError(t, recovered.MaybeHeartbeat(ctx))
	require.Len(t, heartbeatsForTurn(recovered.Diffs(), 2), 3,
		"once height returns after timeout, the producer opens the next turn")
	require.Equal(t, uint64(2), recovered.StateMachine().HeightSyncLatestTurnSeq())
}

func TestRecoverSession_SeedBeforeFirstDurableDiffStaysVolatile(t *testing.T) {
	store := newTestStore(t)
	session, group, hosts, user, seedClients := setupRecoverableSeedSession(t, store, 55)
	ctx := context.Background()

	session.SeedHeightSync(ctx)
	for i, client := range seedClients {
		h, hash, ok := client.ObservedStampNow()
		require.True(t, ok, "slot %d should expose the volatile seed before restart", i)
		require.Equal(t, uint64(55), h)
		require.NotEmpty(t, hash)
	}
	require.Equal(t, uint64(0), session.Nonce())
	require.Empty(t, session.Diffs())
	require.Equal(t, uint64(0), session.StateMachine().HeightSyncLatestTurnSeq())
	require.Equal(t, uint64(0), session.HeartbeatTurnTracker().LastCompletedHeight())
	require.NoError(t, session.Close())

	recovered, _, err := RecoverSession(store, user, signing.NewSecp256k1Verifier(),
		"escrow-1", testutil.RuntimeTestVersion, group,
		recoveryHeartbeatClients(t, group, hosts, user))
	require.NoError(t, err)
	t.Cleanup(func() { _ = recovered.Close() })
	recovered.SetHeightSyncCadence(10, uint64(len(hosts)))

	require.Equal(t, uint64(0), recovered.Nonce())
	require.Empty(t, recovered.Diffs())
	require.Equal(t, uint64(0), recovered.StateMachine().HeightSyncLatestTurnSeq())
	require.Equal(t, uint64(0), recovered.HeartbeatTurnTracker().LastCompletedHeight())
	require.NoError(t, recovered.MaybeHeartbeat(ctx))
	require.Equal(t, uint64(0), recovered.Nonce(), "recovery must not promote a pre-diff seed into durable height")
	require.Equal(t, 1, recovered.HeartbeatSkippedNoHeight())

	recovered.SetObservedHeight(func() (uint64, []byte, bool) { return 55, []byte{0xaa}, true })
	require.NoError(t, recovered.MaybeHeartbeat(ctx))
	hb := heartbeatTxForTurn(recovered.Diffs(), 1)
	require.NotNil(t, hb, "the first durable heartbeat after seeded recovery must still be turn_seq=1")
	require.Equal(t, uint64(55), hb.ObservedHeight)
	require.Nil(t, heartbeatTxForTurn(recovered.Diffs(), 2))
	require.Equal(t, uint64(1), recovered.StateMachine().HeightSyncLatestTurnSeq())
}

func TestRecoverSession_ChangedHeartbeatConfigAffectsFutureCadenceOnly(t *testing.T) {
	store := newTestStore(t)
	var height uint64 = 100
	now := time.Unix(1000, 0).UTC()
	originalOracles := newSessionOracles(3, height, []byte{0xaa})
	session, group, hosts, user := setupRecoverableHeartbeatSessionWithOracles(t, store, &height, &now, originalOracles)
	ctx := context.Background()

	require.NoError(t, session.MaybeHeartbeat(ctx))
	require.NotNil(t, heartbeatTxForTurn(session.Diffs(), 1))
	require.Equal(t, heightsync.TurnComplete, session.HeartbeatTurnTracker().Record(1).State)
	rootBefore, err := session.StateMachine().ComputeStateRoot()
	require.NoError(t, err)
	require.NoError(t, session.Close())

	shortCfg := heightsync.HeartbeatConfig{Interval: 40 * time.Millisecond}
	recoveredOracles := newSessionOracles(len(hosts), height, []byte{0xaa})
	recovered, recSM, err := RecoverSession(store, user, signing.NewSecp256k1Verifier(),
		"escrow-1", testutil.RuntimeTestVersion, group,
		recoveryHeartbeatClientsWithOracles(t, group, hosts, user, recoveredOracles),
		state.WithHeartbeatConfig(shortCfg))
	require.NoError(t, err)
	t.Cleanup(func() { _ = recovered.Close() })
	recovered.SetHeightSyncCadence(10, uint64(len(hosts)))
	recovered.SetObservedHeight(func() (uint64, []byte, bool) {
		h := height
		if h == 0 {
			return 0, nil, false
		}
		return h, []byte{0xaa}, true
	})
	recovered.clock = func() time.Time { return now }

	rootAfter, err := recSM.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, rootBefore, rootAfter, "changed runtime config must not affect durable replay/root")
	require.Equal(t, shortCfg.Interval, recovered.heartbeat.Config().Interval)
	require.Less(t, recovered.heartbeat.Config().Interval, heightsync.DefaultHeartbeatConfig().Interval)

	height = 101
	setSessionOraclesHeight(recoveredOracles, height)
	require.NoError(t, recovered.MaybeHeartbeat(ctx))
	require.NotNil(t, heartbeatTxForTurn(recovered.Diffs(), 2))
	require.Equal(t, heightsync.TurnComplete, recovered.HeartbeatTurnTracker().Record(2).State)

	now = now.Add(shortCfg.Interval - time.Millisecond)
	height = 102
	setSessionOraclesHeight(recoveredOracles, height)
	require.NoError(t, recovered.MaybeHeartbeat(ctx))
	require.Nil(t, heartbeatTxForTurn(recovered.Diffs(), 3),
		"future producer decisions should still honor the recovered overlay interval")

	now = now.Add(2 * time.Millisecond)
	require.NoError(t, recovered.MaybeHeartbeat(ctx))
	require.NotNil(t, heartbeatTxForTurn(recovered.Diffs(), 3),
		"after the recovered overlay interval elapses, the next turn should open")
}

func TestRecoverSession_HostRestartLosesLocalHeartbeatAckMempool(t *testing.T) {
	store := newTestStore(t)
	var height uint64 = 100
	now := time.Unix(1000, 0).UTC()
	session, group, hosts, user := setupRecoverableHeartbeatSession(t, store, &height, &now)
	ctx := context.Background()

	span, err := session.composeHeartbeatSpan()
	require.NoError(t, err)
	require.Len(t, span, 3)
	targetHostIdx := span[0].hostIdx
	targetClient := &dropHeightAckTurnClient{
		HostClient: session.clients[targetHostIdx],
		turnSeq:    1,
		slotID:     uint32(targetHostIdx),
	}
	session.clients[targetHostIdx] = targetClient

	session.dispatchHeartbeatSpan(ctx, span)
	require.True(t, targetClient.Dropped(),
		"target host generated its turn-1 ack, but it was lost with the local host mempool")
	require.Len(t, heightAcksInTxs(session.PendingTxs()), len(hosts)-1)

	require.NoError(t, session.flushHeartbeatAckRounds(ctx))
	require.Empty(t, session.PendingTxs())
	require.Len(t, heightAcksForTurn(session.Diffs(), 1), len(hosts)-1)
	rec1 := session.HeartbeatTurnTracker().Record(1)
	require.NotNil(t, rec1)
	require.Equal(t, heightsync.TurnComplete, rec1.State,
		"the lost host-local ack should not keep quorum-complete turns stuck")
	require.Len(t, rec1.Acks, len(hosts)-1)

	freshTarget := recoveryHeartbeatClients(t, group, hosts, user)[targetHostIdx]
	restartedTarget := &dropHeightAckTurnClient{
		HostClient: freshTarget,
		turnSeq:    1,
		slotID:     uint32(targetHostIdx),
	}
	session.clients[targetHostIdx] = restartedTarget
	session.mu.Lock()
	session.hostSyncNonce[targetHostIdx] = 0
	session.mu.Unlock()

	require.NoError(t, session.sendCatchUp(ctx, targetHostIdx))
	require.True(t, restartedTarget.Dropped(),
		"catch-up to a restarted host must not let the lost old ack re-enter pending")
	require.Empty(t, heightAcksInTxs(session.PendingTxs()))
	require.Equal(t, session.Nonce(), freshTarget.(*InProcessClient).Host.LatestNonce())

	height = 101
	now = now.Add(session.heartbeat.Config().TurnTimeout + time.Second)
	require.NoError(t, session.MaybeHeartbeat(ctx))
	hb2 := heartbeatTxForTurn(session.Diffs(), 2)
	require.NotNil(t, hb2)
	statuses := syncVectorStatuses(hb2.SyncVector)
	require.Equal(t, types.AckStatus_MISSING, statuses[targetHostIdx],
		"the host-restart ack loss is reported as a missing/no-blame slot in the next vector")
	for slot := range hosts {
		if slot == targetHostIdx {
			continue
		}
		require.Equal(t, types.AckStatus_ACKED, statuses[slot], "slot %d", slot)
	}
	require.Len(t, heartbeatsForTurn(session.Diffs(), 1), len(hosts))
	require.Len(t, heartbeatsForTurn(session.Diffs(), 2), len(hosts))
	require.Empty(t, heartbeatsForTurn(session.Diffs(), 3))
	require.Equal(t, uint64(2), session.StateMachine().HeightSyncLatestTurnSeq())
	require.Equal(t, heightsync.TurnComplete, session.HeartbeatTurnTracker().Record(2).State)
	require.Empty(t, session.PendingTxs())
}

func TestRecoverSession_LateAckAfterTurnPruneComposesAndApplies(t *testing.T) {
	store := newTestStore(t)
	const numHosts = 3
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	finalNonce := heightsync.DefaultTurnRetain + 5
	for nonce := uint64(1); nonce <= finalNonce; nonce++ {
		txs := []*types.DevshardTx{{
			Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
				TurnSeq:  nonce,
				SlotsNum: numHosts,
				Reason:   "quiet_session",
			}},
		}}
		root, applied, err := sm.ApplyLocalBestEffort(nonce, txs)
		require.NoError(t, err)
		require.Len(t, applied, 1)
		diff := testutil.SignDiffWithRoot(t, user, "escrow-1", nonce, applied, root)
		require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{Diff: diff, StateHash: root}))
	}
	require.Nil(t, sm.HeightSyncTurnRecord(1), "turn 1 should be pruned from the live record window")
	smTracker := sm.HeightSyncCloneTurnTracker()
	require.NotNil(t, smTracker)
	seq, ok := smTracker.HeartbeatTurn(1)
	require.True(t, ok, "heartbeatAt must keep the turn mapping after record prune")
	require.Equal(t, uint64(1), seq)
	saveSnapshot(store, sm, "escrow-1", finalNonce, map[int]uint64{0: finalNonce, 1: finalNonce, 2: finalNonce})

	recovered, _, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group,
		buildRecoveryClients(t, hosts, group, user))
	require.NoError(t, err)
	t.Cleanup(func() { _ = recovered.Close() })
	require.Equal(t, finalNonce, recovered.Nonce())
	recTracker := recovered.HeartbeatTurnTracker()
	require.Nil(t, recTracker.Record(1), "snapshot/recover should keep turn 1 pruned")
	seq, ok = recTracker.HeartbeatTurn(1)
	require.True(t, ok, "recovered heartbeatAt should still admit late acks for pruned turns")
	require.Equal(t, uint64(1), seq)

	ack := &types.MsgHeightAck{
		TurnSeq:           1,
		RefNonce:          1,
		SlotId:            0,
		ObservedHeight:    50,
		ObservedBlockHash: []byte{0xaa},
		SyncState:         types.SyncState_SYNCED,
		PeerSeen:          []byte{0xff},
	}
	require.NoError(t, heightsync.SignAck(hosts[0], ack))
	require.NoError(t, recovered.ProcessResponse(0, &host.HostResponse{
		Nonce: recovered.Nonce(),
		Mempool: []*types.DevshardTx{{
			Tx: &types.DevshardTx_HeightAck{HeightAck: ack},
		}},
	}, recovered.Nonce()))
	require.Len(t, heightAcksInTxs(recovered.PendingTxs()), 1)

	recovered.mu.Lock()
	lateAckDiff, _, err := recovered.composeDiffLocked(nil)
	recovered.mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, finalNonce+1, lateAckDiff.Nonce)
	require.Len(t, heightAcksInDiffs([]types.Diff{lateAckDiff}), 1)
	require.Equal(t, finalNonce+1, recovered.Nonce())
	require.Empty(t, recovered.PendingTxs())
	require.Len(t, heightAcksForTurn(recovered.Diffs(), 1), 1)
	recTracker = recovered.HeartbeatTurnTracker()
	require.Nil(t, recTracker.Record(1), "late ack must not unprune or pin an old turn record")
	seq, ok = recTracker.HeartbeatTurn(1)
	require.True(t, ok)
	require.Equal(t, uint64(1), seq)
	require.Equal(t, finalNonce, recTracker.MaxTurnSeq(),
		"late ack for a pruned old turn must not rewind or advance max turn_seq")
	require.Equal(t, finalNonce, recovered.StateMachine().HeightSyncLatestTurnSeq())
}

func TestRecoverSession_OpenTurnRepairProbeStateIsVolatile(t *testing.T) {
	store := newTestStore(t)
	const numHosts = 3
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	buildSM := func() *state.StateMachine {
		t.Helper()
		return newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	}
	liveSM := buildSM()
	appendHeartbeatDiff(t, store, liveSM, user, 1, 1, 100)

	recovered, recSM, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group,
		buildRecoveryClients(t, hosts, group, user))
	require.NoError(t, err)
	t.Cleanup(func() { _ = recovered.Close() })
	rec := recSM.HeightSyncTurnRecord(1)
	require.NotNil(t, rec)
	require.Equal(t, heightsync.TurnOpen, rec.State)

	oracle := &sessionOracle{hash: []byte{0xbb}}
	oracle.height.Store(200)
	oldHost, err := host.NewHost(recSM, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil,
		host.WithChainOracle(oracle),
		host.WithRepairConfig(heightsync.RepairConfig{Stagger: 0, MaxProbesPerWindow: numHosts}),
	)
	require.NoError(t, err)
	oldHost.RepairBudget().Record(1, 1, heightsync.RepairOutcomeUnreachable)
	require.Equal(t, 1, oldHost.RepairBudget().ProbedCount(),
		"pre-restart repair budget state is host-local")

	cfg := heightsync.DefaultHeartbeatConfig()
	timeoutHeight := uint64(100) + cfg.AckDeadlineBlocks + 1
	appendHeartbeatDiff(t, store, recSM, user, 2, 2, timeoutHeight)
	rec = recSM.HeightSyncTurnRecord(1)
	require.NotNil(t, rec)
	require.Equal(t, heightsync.TurnDegraded, rec.State)
	require.ElementsMatch(t, []uint32{0, 1, 2}, recSM.HeightSyncMissingAcks(1))
	require.NotEmpty(t, recSM.HeightSyncRepairDue())

	recoveredHost, err := host.NewHost(recSM, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil,
		host.WithChainOracle(oracle),
		host.WithRepairConfig(heightsync.RepairConfig{Stagger: 0, MaxProbesPerWindow: numHosts}),
	)
	require.NoError(t, err)
	require.Zero(t, recoveredHost.RepairBudget().ProbedCount(),
		"fresh recovered host must not inherit volatile repair budget state")

	var probes atomic.Int64
	recoveredHost.SetRepairProbe(func(ctx context.Context, targetSlot uint32, req *heightsync.RepairRequest) (*heightsync.RepairResponse, error) {
		require.Equal(t, uint64(1), req.TurnSeq)
		require.NotEqual(t, uint32(0), targetSlot, "host must not probe its own missing slot")
		probes.Add(1)
		return nil, context.DeadlineExceeded
	})

	recoveredHost.MaybeRepair(context.Background())
	require.Equal(t, int64(2), probes.Load())
	require.Equal(t, 2, recoveredHost.RepairBudget().ProbedCount())
	require.Equal(t, 2, recoveredHost.RepairBudget().Count(heightsync.RepairOutcomeUnreachable))

	recoveredHost.MaybeRepair(context.Background())
	require.Equal(t, int64(2), probes.Load(), "second repair pass must not double-probe the same recovered turn slots")
	require.Equal(t, 2, recoveredHost.RepairBudget().ProbedCount())
	require.Equal(t, 2, recoveredHost.RepairBudget().Count(heightsync.RepairOutcomeUnreachable))
}

func TestRecoverSession_ReplayWithoutEnvelopeAfterEdgeMarkIsDeterministic(t *testing.T) {
	store := newTestStore(t)
	const numHosts = 3
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	liveSM := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	hbDiff := appendHeartbeatDiff(t, store, liveSM, user, 1, 1, 250)
	ackTxs := make([]*types.DevshardTx, 0, len(hosts))
	for slot := range hosts {
		ack := &types.MsgHeightAck{
			TurnSeq:           1,
			RefNonce:          1,
			SlotId:            uint32(slot),
			ObservedHeight:    250,
			ObservedBlockHash: []byte{0x02, 0x50},
			SyncState:         types.SyncState_SYNCED,
			PeerSeen:          []byte{0xff},
		}
		require.NoError(t, heightsync.SignAck(hosts[slot], ack))
		ackTxs = append(ackTxs, &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}})
	}

	edgeMarks := heightsync.CheckEnvelopeBinding(heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   ackTxs,
		Sec: &heightsync.HeightSyncSection{
			ProofType:           heightsync.AnchorProofType,
			MainnetHeight:       250,
			MainnetBlockHashHex: "0250",
			Direction:           "response",
		},
		LocalAligned: 50_000,
	}, heightsync.DefaultHeartbeatConfig())
	require.NotEmpty(t, edgeMarks, "fixture must exercise an edge-only envelope mark")

	root2, applied, err := liveSM.ApplyLocalBestEffort(2, ackTxs)
	require.NoError(t, err)
	require.Len(t, applied, len(hosts))
	ackDiff := testutil.SignDiffWithRoot(t, user, "escrow-1", 2, applied, root2)
	require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{Diff: ackDiff, StateHash: root2}))
	liveRoot, err := liveSM.ComputeStateRoot()
	require.NoError(t, err)
	require.Empty(t, liveSM.HeightSyncMarks(),
		"edge admission marks must not enter the replayed consensus state")

	recovered, recSM, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group,
		buildRecoveryClients(t, hosts, group, user))
	require.NoError(t, err)
	t.Cleanup(func() { _ = recovered.Close() })
	recoveredRoot, err := recSM.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, liveRoot, recoveredRoot)
	require.Equal(t, uint64(2), recovered.Nonce())
	require.Empty(t, recSM.HeightSyncMarks(),
		"recover replay has no envelope and must not rederive edge-only marks")

	offlineSM := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	_, err = offlineSM.ApplyDiff(hbDiff)
	require.NoError(t, err)
	_, err = offlineSM.ApplyDiff(ackDiff)
	require.NoError(t, err)
	offlineRoot, err := offlineSM.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, recoveredRoot, offlineRoot)
	require.Equal(t, recSM.HeightSyncMarks(), offlineSM.HeightSyncMarks())
}

func TestRecoverSession_HeartbeatEmptyStartsAtTurnOne(t *testing.T) {
	store := newTestStore(t)
	var height uint64 = 100
	now := time.Unix(1000, 0).UTC()
	session, group, hosts, user := setupRecoverableHeartbeatSession(t, store, &height, &now)
	require.Equal(t, uint64(0), session.Nonce())
	require.NoError(t, session.Close())

	recovered := recoverHeartbeatSession(t, store, group, hosts, user, &height)
	t.Cleanup(func() { _ = recovered.Close() })
	require.Equal(t, uint64(0), recovered.StateMachine().HeightSyncLatestTurnSeq())

	require.NoError(t, recovered.MaybeHeartbeat(context.Background()))
	hb := heartbeatTxForTurn(recovered.Diffs(), 1)
	require.NotNil(t, hb)
	require.Nil(t, heartbeatTxForTurn(recovered.Diffs(), 2))
}

func recoveryHeartbeatClients(
	t *testing.T,
	group []types.SlotAssignment,
	hosts []*signing.Secp256k1Signer,
	user *signing.Secp256k1Signer,
) []HostClient {
	t.Helper()
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	clients := make([]HostClient, len(hosts))
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}
	return clients
}

func recoveryHeartbeatClientsWithOracles(
	t *testing.T,
	group []types.SlotAssignment,
	hosts []*signing.Secp256k1Signer,
	user *signing.Secp256k1Signer,
	oracles []*sessionOracle,
) []HostClient {
	t.Helper()
	require.Len(t, oracles, len(hosts))
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	clients := make([]HostClient, len(hosts))
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil,
			host.WithGrace(100),
			host.WithChainOracle(oracles[i]),
		)
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}
	return clients
}

type seededInProcessClient struct {
	*InProcessClient
	seeded atomic.Bool
	height uint64
	hash   []byte
}

type dropHeightAckTurnClient struct {
	HostClient
	dropped atomic.Bool
	turnSeq uint64
	slotID  uint32
}

func (c *dropHeightAckTurnClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	resp, err := c.HostClient.Send(ctx, req, stream, receiptHandler)
	if err != nil || resp == nil {
		return resp, err
	}
	filtered := resp.Mempool[:0]
	for _, tx := range resp.Mempool {
		ack := tx.GetHeightAck()
		if ack != nil && ack.TurnSeq == c.turnSeq && ack.SlotId == c.slotID {
			c.dropped.Store(true)
			continue
		}
		filtered = append(filtered, tx)
	}
	resp.Mempool = filtered
	return resp, nil
}

func (c *dropHeightAckTurnClient) Dropped() bool {
	return c.dropped.Load()
}

func (c *seededInProcessClient) SeedHeightSync(context.Context) (bool, error) {
	c.seeded.Store(true)
	return true, nil
}

func (c *seededInProcessClient) ObservedStampNow() (uint64, []byte, bool) {
	if !c.seeded.Load() {
		return 0, nil, false
	}
	return c.height, append([]byte(nil), c.hash...), true
}

func newSessionOracles(n int, height uint64, hash []byte) []*sessionOracle {
	oracles := make([]*sessionOracle, n)
	for i := range oracles {
		oracles[i] = &sessionOracle{hash: append([]byte(nil), hash...)}
		oracles[i].height.Store(int64(height))
	}
	return oracles
}

func setSessionOraclesHeight(oracles []*sessionOracle, height uint64) {
	for _, oracle := range oracles {
		oracle.height.Store(int64(height))
	}
}

func appendHeartbeatDiff(
	t *testing.T,
	store storage.Storage,
	sm *state.StateMachine,
	user *signing.Secp256k1Signer,
	nonce uint64,
	turnSeq uint64,
	height uint64,
) types.Diff {
	t.Helper()
	txs := []*types.DevshardTx{{
		Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			TurnSeq:           turnSeq,
			ObservedHeight:    height,
			ObservedBlockHash: []byte{0xaa},
			SlotsNum:          uint64(len(sm.SnapshotState().Group)),
			Reason:            "quiet_session",
		}},
	}}
	root, applied, err := sm.ApplyLocalBestEffort(nonce, txs)
	require.NoError(t, err)
	require.Len(t, applied, 1)
	diff := testutil.SignDiffWithRoot(t, user, "escrow-1", nonce, applied, root)
	if store != nil {
		require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{Diff: diff, StateHash: root}))
	}
	return diff
}

func heightAcksInTxs(txs []*types.DevshardTx) []*types.MsgHeightAck {
	var out []*types.MsgHeightAck
	for _, tx := range txs {
		if ack := tx.GetHeightAck(); ack != nil {
			out = append(out, ack)
		}
	}
	return out
}

func heartbeatsForTurn(diffs []types.Diff, turnSeq uint64) []*types.MsgHeartbeat {
	var out []*types.MsgHeartbeat
	for _, d := range diffs {
		for _, tx := range d.Txs {
			if hb := tx.GetHeartbeat(); hb != nil && hb.TurnSeq == turnSeq {
				out = append(out, hb)
			}
		}
	}
	return out
}

func heightAcksForTurn(diffs []types.Diff, turnSeq uint64) []*types.MsgHeightAck {
	var out []*types.MsgHeightAck
	for _, d := range diffs {
		for _, tx := range d.Txs {
			if ack := tx.GetHeightAck(); ack != nil && ack.TurnSeq == turnSeq {
				out = append(out, ack)
			}
		}
	}
	return out
}

func syncVectorStatuses(vec []*types.SyncVectorEntry) map[int]types.AckStatus {
	out := make(map[int]types.AckStatus, len(vec))
	for _, ent := range vec {
		out[int(ent.SlotId)] = ent.Status
	}
	return out
}

func ackNoncesBySlot(rec *heightsync.SyncTurnRecord) map[uint32]uint64 {
	out := make(map[uint32]uint64, len(rec.Acks))
	for slot, ack := range rec.Acks {
		out[slot] = ack.Nonce
	}
	return out
}

func TestRecoverSession_WarmKeyDelta(t *testing.T) {
	store := newTestStore(t)
	numHosts := 3

	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	warmKey := testutil.MustGenerateKey(t)
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	// Inference 1 executor = slot 1%3 = 1 -> hosts[1].
	executorSlot := uint32(1 % numHosts)

	// Resolver recognizes warmKey as authorized for the executor's cold key.
	resolver := func(warm, cold string) (bool, error) {
		if warm == warmKey.Address() && cold == hosts[executorSlot].Address() {
			return true, nil
		}
		return false, nil
	}

	sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier,
		state.WithWarmKeyResolver(resolver),
	)

	// Nonce 1: StartInference + ConfirmStart (status -> Started). No warm keys yet.
	confirmSig := testutil.SignExecutorReceipt(t, hosts[executorSlot], "escrow-1", 1,
		testutil.TestPromptHash[:], "llama", 100, testutil.TestMaxTokens, 1000, 2000)
	txs1 := []*types.DevshardTx{
		testutil.StartTx(1),
		{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
			InferenceId: 1, ExecutorSig: confirmSig, ConfirmedAt: 2000,
		}}},
	}
	root1, err := sm.ApplyLocal(1, txs1)
	require.NoError(t, err)

	diff1 := testutil.SignDiffWithRoot(t, user, "escrow-1", 1, txs1, root1)
	require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{
		Diff: diff1, StateHash: root1,
	}))

	// Nonce 2: FinishInference signed by warmKey. The resolver resolves during
	// ApplyLocal, caching the warm key in state. Capture delta.
	warmBefore := sm.WarmKeys()
	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("resp"),
		InputTokens: 10, OutputTokens: 20, ExecutorSlot: executorSlot, EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, warmKey, finishMsg)

	txs2 := []*types.DevshardTx{{Tx: &types.DevshardTx_FinishInference{FinishInference: finishMsg}}}
	root2, err := sm.ApplyLocal(2, txs2)
	require.NoError(t, err)
	warmAfter := sm.WarmKeys()
	delta := types.ComputeWarmKeyDelta(warmBefore, warmAfter)
	require.NotNil(t, delta, "warm key delta must be non-nil after resolver resolves")

	diff2 := testutil.SignDiffWithRoot(t, user, "escrow-1", 2, txs2, root2)
	require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{
		Diff: diff2, StateHash: root2, WarmKeyDelta: delta,
	}))

	// Recover WITHOUT a resolver. Warm keys must come from stored delta only.
	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm2 := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, hErr := host.NewHost(sm2, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil)
		require.NoError(t, hErr)
		clients[i] = &InProcessClient{Host: h}
	}

	session, recSM, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group, clients)
	require.NoError(t, err)
	require.Equal(t, uint64(2), session.Nonce())

	// State root after recovery must match original.
	recRoot, err := recSM.ComputeStateRoot()
	require.NoError(t, err)
	origRoot, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, origRoot, recRoot)
}

func TestRecoverSession_WithSMOptions(t *testing.T) {
	store := newTestStore(t)
	numHosts := 3

	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil)
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	resolverCalled := false
	resolver := func(warm, cold string) (bool, error) {
		resolverCalled = true
		return false, nil
	}

	// Recover with a warm key resolver option.
	session, recSM, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group, clients,
		state.WithWarmKeyResolver(resolver),
	)
	require.NoError(t, err)
	require.Equal(t, uint64(0), session.Nonce())

	// The resolver should be wired: CheckWarmKey triggers it.
	recSM.CheckWarmKey("unknown-addr", hosts[0].Address())
	require.True(t, resolverCalled, "resolver must be called after recovery with WithWarmKeyResolver")
}

func TestRecoverSession_SignaturesRestored(t *testing.T) {
	store := newTestStore(t)
	numHosts := 3
	numInferences := 3

	group, hosts, user := setupRecoverableSession(t, numHosts, numInferences, store)

	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(10))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	session, _, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group, clients)
	require.NoError(t, err)

	// Each inference gets a signature from the executor host.
	sigs := session.Signatures()
	hasSigs := false
	for _, nonceSigs := range sigs {
		if len(nonceSigs) > 0 {
			hasSigs = true
			break
		}
	}
	require.True(t, hasSigs, "recovered session should have signatures")

	// Verify the prompt hash is computed correctly for test data (sanity check).
	_, err = devshard.CanonicalPromptHash(testutil.TestPrompt)
	require.NoError(t, err)
}

// TestRecoverSession_SnapshotOnly_RestoresSignatures covers reboot after a
// final-nonce snapshot flush: recovery skips diff replay, so signatures must
// be reloaded from the store (otherwise settlement sees empty s.signatures).
func TestRecoverSession_SnapshotOnly_RestoresSignatures(t *testing.T) {
	store := newTestStore(t)
	numHosts := 3
	numInferences := 4

	group, hosts, user := setupRecoverableSession(t, numHosts, numInferences, store)
	finalNonce := uint64(numInferences)

	// Ensure final-nonce signatures are in the store (Phase B / processResponse path).
	stored, err := store.GetSignatures("escrow-1", finalNonce)
	require.NoError(t, err)
	if len(stored) == 0 {
		for slot := uint32(0); slot < uint32(numHosts); slot++ {
			require.NoError(t, store.AddSignature("escrow-1", finalNonce, slot, []byte{byte(slot + 1), 9, 9, 9}))
		}
		stored, err = store.GetSignatures("escrow-1", finalNonce)
		require.NoError(t, err)
	}
	require.NotEmpty(t, stored, "precondition: store must have signatures at final nonce")

	// Rebuild once to get a session at LatestNonce, mark every host caught up,
	// then flush so the next recover takes the snapshot-only early-return path.
	verifier := signing.NewSecp256k1Verifier()
	live, _, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group,
		buildRecoveryClients(t, hosts, group, user))
	require.NoError(t, err)
	require.Equal(t, finalNonce, live.Nonce())
	live.mu.Lock()
	for i := 0; i < numHosts; i++ {
		live.hostSyncNonce[i] = finalNonce
	}
	live.mu.Unlock()
	require.NoError(t, live.FlushSnapshot())

	spy := &replaySpyStore{Storage: store}
	rec, _, err := RecoverSession(spy, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group,
		buildRecoveryClients(t, hosts, group, user))
	require.NoError(t, err)
	require.Equal(t, finalNonce, rec.Nonce())
	require.Zero(t, spy.replayedRecords(finalNonce), "must use snapshot-only early return")
	require.Empty(t, rec.Diffs(), "snapshot-only recovery keeps diffs empty when all hosts are caught up")

	got := rec.Signatures()[finalNonce]
	require.NotEmpty(t, got, "final-nonce signatures must be restored from store")
	for slotID, want := range stored {
		require.Equal(t, want, got[slotID], "slot %d", slotID)
	}
}

func TestRecoverSession_SnapshotOnly_RestoresPendingTxDedupKeys(t *testing.T) {
	store := newTestStore(t)
	var height uint64 = 100
	now := time.Unix(1000, 0).UTC()
	session, group, hosts, user := setupRecoverableHeartbeatSession(t, store, &height, &now)
	ctx := context.Background()

	span, err := session.composeHeartbeatSpan()
	require.NoError(t, err)
	require.Len(t, span, 3)
	session.dispatchHeartbeatSpan(ctx, span)
	require.Len(t, heightAcksInTxs(session.PendingTxs()), 3)
	session.mu.Lock()
	ackDiff, _, err := session.composeDiffLocked(nil)
	for i := range hosts {
		session.hostSyncNonce[i] = ackDiff.Nonce
	}
	session.mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, uint64(4), ackDiff.Nonce)
	acks := heightAcksInDiffs([]types.Diff{ackDiff})
	require.Len(t, acks, 3)
	require.NoError(t, session.FlushSnapshot())
	require.NoError(t, session.Close())

	spy := &replaySpyStore{Storage: store}
	recovered, _, err := RecoverSession(spy, user, signing.NewSecp256k1Verifier(),
		"escrow-1", testutil.RuntimeTestVersion, group,
		recoveryHeartbeatClients(t, group, hosts, user))
	require.NoError(t, err)
	t.Cleanup(func() { _ = recovered.Close() })
	require.Equal(t, ackDiff.Nonce, recovered.Nonce())
	require.Zero(t, spy.replayedRecords(ackDiff.Nonce), "must use snapshot-only early return")
	require.Empty(t, recovered.Diffs(), "snapshot-only recovery keeps diffs empty when all hosts are caught up")

	rootBefore, err := recovered.StateMachine().ComputeStateRoot()
	require.NoError(t, err)
	require.NoError(t, recovered.ProcessResponse(int(acks[0].SlotId), &host.HostResponse{
		Nonce: recovered.Nonce(),
		Mempool: []*types.DevshardTx{
			{Tx: &types.DevshardTx_HeightAck{HeightAck: acks[0]}},
		},
	}, recovered.Nonce()))
	require.Empty(t, heightAcksInTxs(recovered.PendingTxs()),
		"duplicate height_ack from a durable pre-snapshot diff must not re-enter pending")
	require.Equal(t, ackDiff.Nonce, recovered.Nonce())
	rootAfter, err := recovered.StateMachine().ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, rootBefore, rootAfter)
}

func TestRecoverSession_SnapshotOnly_RestoresPendingTxDedupKeysForHostProposedTypes(t *testing.T) {
	tests := []struct {
		name string
		tx   *types.DevshardTx
	}{
		{
			name: "finish",
			tx: &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{
				FinishInference: &types.MsgFinishInference{InferenceId: 7, ExecutorSlot: 1, EscrowId: "escrow-1"},
			}},
		},
		{
			name: "confirm",
			tx: &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{
				ConfirmStart: &types.MsgConfirmStart{InferenceId: 7},
			}},
		},
		{
			name: "validation",
			tx: &types.DevshardTx{Tx: &types.DevshardTx_Validation{
				Validation: &types.MsgValidation{InferenceId: 7, ValidatorSlot: 1, EscrowId: "escrow-1"},
			}},
		},
		{
			name: "validation_vote",
			tx: &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{
				ValidationVote: &types.MsgValidationVote{InferenceId: 7, VoterSlot: 2, EscrowId: "escrow-1"},
			}},
		},
		{
			name: "reveal_seed",
			tx: &types.DevshardTx{Tx: &types.DevshardTx_RevealSeed{
				RevealSeed: &types.MsgRevealSeed{SlotId: 2, EscrowId: "escrow-1"},
			}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recovered := recoverSnapshotOnlyWithDurableHostTx(t, tc.tx)
			t.Cleanup(func() { _ = recovered.Close() })

			rootBefore, err := recovered.StateMachine().ComputeStateRoot()
			require.NoError(t, err)
			require.NoError(t, recovered.ProcessResponse(0, &host.HostResponse{
				Nonce:   recovered.Nonce(),
				Mempool: []*types.DevshardTx{tc.tx},
			}, recovered.Nonce()))
			require.Empty(t, recovered.PendingTxs(),
				"duplicate %s from a durable pre-snapshot diff must not re-enter pending", tc.name)
			require.Equal(t, uint64(1), recovered.Nonce())
			rootAfter, err := recovered.StateMachine().ComputeStateRoot()
			require.NoError(t, err)
			require.Equal(t, rootBefore, rootAfter)
		})
	}
}

func TestRecoverSession_SnapshotOnly_RestoresSealedInferenceIndexes(t *testing.T) {
	store := newTestStore(t)
	const escrowID = "escrow-1"
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       escrowID,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	sm, err := state.NewStateMachine(escrowID, config, group, 100000, user.Address(), verifier, store,
		state.WithStateRootAndProtocolVersion(testutil.RuntimeTestVersion))
	require.NoError(t, err)
	appendAppliedDiff := func(nonce uint64, txs []*types.DevshardTx) {
		t.Helper()
		root, err := sm.ApplyLocal(nonce, txs)
		require.NoError(t, err)
		diff := testutil.SignDiffWithRoot(t, user, escrowID, nonce, txs, root)
		require.NoError(t, store.AppendDiff(escrowID, types.DiffRecord{Diff: diff, StateHash: root}))
	}

	promptHash, err := devshard.CanonicalPromptHash(testutil.TestPrompt)
	require.NoError(t, err)
	appendAppliedDiff(1, []*types.DevshardTx{{Tx: &types.DevshardTx_StartInference{
		StartInference: &types.MsgStartInference{
			InferenceId: 1, PromptHash: promptHash, Model: "llama",
			InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
		},
	}}})
	execSig := testutil.SignExecutorReceipt(t, hosts[1], escrowID, 1, promptHash, "llama", 100, testutil.TestMaxTokens, 1000, 2000)
	appendAppliedDiff(2, []*types.DevshardTx{{Tx: &types.DevshardTx_ConfirmStart{
		ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 2000},
	}}})
	finish := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("response"),
		InputTokens: 10, OutputTokens: 20, ExecutorSlot: 1, EscrowId: escrowID,
	}
	finish.ProposerSig = testutil.SignProposerTx(t, hosts[1], finish)
	appendAppliedDiff(3, []*types.DevshardTx{{Tx: &types.DevshardTx_FinishInference{FinishInference: finish}}})

	require.NoError(t, sm.SealInference(1))
	_, live := sm.SnapshotState().Inferences[1]
	require.False(t, live, "precondition: sealed inference must be absent from live state")
	before, ok := sm.ExportAllInferenceRecords()[1]
	require.True(t, ok)
	require.Equal(t, types.StatusFinished, before.Status)
	row, ok, err := store.GetSealedInference(escrowID, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, row.ObsPresent)

	saveSnapshot(store, sm, escrowID, 3, map[int]uint64{0: 3, 1: 3, 2: 3})
	spy := &replaySpyStore{Storage: store}
	recovered, recSM, err := RecoverSession(spy, user, verifier, escrowID, testutil.RuntimeTestVersion, group,
		buildRecoveryClients(t, hosts, group, user))
	require.NoError(t, err)
	t.Cleanup(func() { _ = recovered.Close() })
	require.Equal(t, uint64(3), recovered.Nonce())
	require.Empty(t, recovered.Diffs(), "all hosts are caught up, so recovery should be snapshot-only")
	require.Zero(t, spy.replayedRecords(3), "must use snapshot-only early return")

	records := recSM.ExportAllInferenceRecords()
	got, ok := records[1]
	require.True(t, ok, "sealed inference must remain visible after snapshot-only recovery")
	require.Equal(t, types.StatusFinished, got.Status)
	require.Equal(t, "llama", got.Model)
	require.Equal(t, uint64(10), got.InputTokens)
	require.Equal(t, uint64(20), got.OutputTokens)
	sealed, ok := recSM.LookupSealedInference(1)
	require.True(t, ok)
	require.Equal(t, got.Status, sealed.Status)
	_, live = recSM.SnapshotState().Inferences[1]
	require.False(t, live, "sealed inference must not be resurrected into live state")

	lateValidation := &types.MsgValidation{
		InferenceId:   1,
		ValidatorSlot: 2,
		Valid:         false,
		EscrowId:      escrowID,
	}
	lateValidation.ProposerSig = testutil.SignProposerTx(t, hosts[2], lateValidation)
	_, err = recSM.ApplyLocal(4, []*types.DevshardTx{{Tx: &types.DevshardTx_Validation{
		Validation: lateValidation,
	}}})
	require.ErrorIs(t, err, types.ErrInferenceSealed)
}

func recoverSnapshotOnlyWithDurableHostTx(t *testing.T, tx *types.DevshardTx) *Session {
	t.Helper()
	store := newTestStore(t)
	const escrowID = "escrow-1"
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       escrowID,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))
	diff := types.Diff{Nonce: 1, Txs: []*types.DevshardTx{tx}}
	require.NoError(t, store.AppendDiff(escrowID, types.DiffRecord{Diff: diff}))

	stateSnapshot := newTestStateMachine(t, escrowID, config, group, 100000, user.Address(), verifier).SnapshotState()
	stateSnapshot.LatestNonce = 1
	blob, err := json.Marshal(sessionSnapshot{
		State:         &stateSnapshot,
		HostSyncNonce: map[int]uint64{0: 1, 1: 1, 2: 1},
	})
	require.NoError(t, err)
	require.NoError(t, store.SaveSnapshot(escrowID, 1, blob))

	spy := &replaySpyStore{Storage: store}
	recovered, _, err := RecoverSession(spy, user, verifier, escrowID, testutil.RuntimeTestVersion, group,
		buildRecoveryClients(t, hosts, group, user))
	require.NoError(t, err)
	require.Equal(t, uint64(1), recovered.Nonce())
	require.Empty(t, recovered.Diffs(), "all hosts are caught up, so recovery should be snapshot-only")
	require.Zero(t, spy.replayedRecords(1), "must use snapshot-only early return")
	return recovered
}

// buildRecoveryClients creates a fresh set of in-process host clients for
// recovery, mirroring setupRecoverableSession's client factory.
func buildRecoveryClients(t *testing.T, hosts []*signing.Secp256k1Signer, group []types.SlotAssignment, user *signing.Secp256k1Signer) []HostClient {
	t.Helper()
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	clients := make([]HostClient, len(hosts))
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(10))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}
	return clients
}

// TestRecoverSession_NewFormatSnapshot_RestoresHostCursor verifies that
// when the snapshot was written in the new wrapper format with a populated
// HostSyncNonce, recovery restores that cursor verbatim into the session.
// This is the primary fix for the post-restart "invalid nonce: must be
// sequential" cascade observed on mainnet 2026-04-24.
func TestRecoverSession_NewFormatSnapshot_RestoresHostCursor(t *testing.T) {
	store := newTestStore(t)
	numHosts := 3
	numInferences := 4

	group, hosts, user := setupRecoverableSession(t, numHosts, numInferences, store)

	verifier := signing.NewSecp256k1Verifier()
	config := testutil.DefaultConfig(numHosts)
	sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	records, err := store.GetDiffs("escrow-1", 1, uint64(numInferences))
	require.NoError(t, err)
	for _, rec := range records {
		_, err := sm.ApplyLocal(rec.Nonce, rec.Txs)
		require.NoError(t, err)
	}
	cursor := map[int]uint64{
		0: uint64(numInferences) - 2,
		1: uint64(numInferences),
		2: uint64(numInferences) - 1,
	}
	saveSnapshot(store, sm, "escrow-1", uint64(numInferences), cursor)

	session, _, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group, buildRecoveryClients(t, hosts, group, user))
	require.NoError(t, err)
	require.Equal(t, uint64(numInferences), session.Nonce())

	session.mu.Lock()
	got := make(map[int]uint64, len(session.hostSyncNonce))
	for k, v := range session.hostSyncNonce {
		got[k] = v
	}
	session.mu.Unlock()
	require.Equal(t, cursor, got, "hostSyncNonce must round-trip through snapshot")
}

// TestRecoverSession_NewFormatSnapshot_BackfillsStrandedHost reproduces
// the mainnet bug: a snapshot is taken at nonce N; the proxy restarts;
// host X had only applied diffs up to N-2 because it was offline during
// nonce N-1 and N. Recovery must keep diffs (X.cursor, N] in sess.diffs
// so the next outgoing request can resend the gap, otherwise the host
// rejects the new diff with "invalid nonce: must be sequential".
func TestRecoverSession_NewFormatSnapshot_BackfillsStrandedHost(t *testing.T) {
	store := newTestStore(t)
	numHosts := 3
	numInferences := 6

	group, hosts, user := setupRecoverableSession(t, numHosts, numInferences, store)

	verifier := signing.NewSecp256k1Verifier()
	config := testutil.DefaultConfig(numHosts)
	sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	records, err := store.GetDiffs("escrow-1", 1, uint64(numInferences))
	require.NoError(t, err)
	for _, rec := range records {
		_, err := sm.ApplyLocal(rec.Nonce, rec.Txs)
		require.NoError(t, err)
	}
	stranded := uint64(numInferences) - 3
	cursor := map[int]uint64{
		0: stranded,
		1: uint64(numInferences),
		2: uint64(numInferences),
	}
	saveSnapshot(store, sm, "escrow-1", uint64(numInferences), cursor)

	session, _, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group, buildRecoveryClients(t, hosts, group, user))
	require.NoError(t, err)
	require.Equal(t, uint64(numInferences), session.Nonce())

	diffs := session.Diffs()
	require.Equal(t, int(uint64(numInferences)-stranded), len(diffs),
		"sess.diffs must span (stranded=%d, latest=%d] for catch-up", stranded, numInferences)
	require.Equal(t, stranded+1, diffs[0].Nonce, "first diff must be stranded+1")
	require.Equal(t, uint64(numInferences), diffs[len(diffs)-1].Nonce, "last diff must be latest")
}

func TestRecoverSession_NewFormatSnapshot_ProcessResponseUsesActualDiffNonce(t *testing.T) {
	store := newTestStore(t)
	numHosts := 3
	numInferences := 6

	group, hosts, user := setupRecoverableSession(t, numHosts, numInferences, store)

	verifier := signing.NewSecp256k1Verifier()
	config := testutil.DefaultConfig(numHosts)
	sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	records, err := store.GetDiffs("escrow-1", 1, uint64(numInferences))
	require.NoError(t, err)
	for _, rec := range records {
		_, err := sm.ApplyLocal(rec.Nonce, rec.Txs)
		require.NoError(t, err)
	}

	cursor := map[int]uint64{
		0: uint64(numInferences) - 3,
		1: uint64(numInferences),
		2: uint64(numInferences),
	}
	saveSnapshot(store, sm, "escrow-1", uint64(numInferences), cursor)

	session, _, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group, buildRecoveryClients(t, hosts, group, user))
	require.NoError(t, err)

	diffs := session.Diffs()
	require.Len(t, diffs, 3)
	require.Equal(t, uint64(4), diffs[0].Nonce)
	require.Equal(t, uint64(5), diffs[1].Nonce)

	err = session.ProcessResponse(0, &host.HostResponse{
		Nonce:     diffs[1].Nonce,
		StateHash: diffs[1].PostStateRoot,
	}, diffs[1].Nonce)
	require.NoError(t, err)
}

// TestRecoverSession_LegacySnapshot_BackwardCompat verifies that a
// snapshot blob written in the old bare-EscrowState format is loaded
// successfully, that the host cursor is treated as unknown (forcing
// full diff backfill into sess.diffs), and that the snapshot is
// upgraded to the new wrapper format on disk so subsequent restarts
// pay the full-backfill cost only once.
func TestRecoverSession_LegacySnapshot_BackwardCompat(t *testing.T) {
	store := newTestStore(t)
	numHosts := 3
	numInferences := 5

	group, hosts, user := setupRecoverableSession(t, numHosts, numInferences, store)

	verifier := signing.NewSecp256k1Verifier()
	config := testutil.DefaultConfig(numHosts)
	sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	records, err := store.GetDiffs("escrow-1", 1, uint64(numInferences))
	require.NoError(t, err)
	for _, rec := range records {
		_, err := sm.ApplyLocal(rec.Nonce, rec.Txs)
		require.NoError(t, err)
	}
	bareData, err := json.Marshal(sm.ExportState())
	require.NoError(t, err)
	require.NoError(t, store.SaveSnapshot("escrow-1", uint64(numInferences), bareData))

	session, _, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group, buildRecoveryClients(t, hosts, group, user))
	require.NoError(t, err)
	require.Equal(t, uint64(numInferences), session.Nonce())

	session.mu.Lock()
	cursorLen := len(session.hostSyncNonce)
	session.mu.Unlock()
	require.Equal(t, 0, cursorLen, "legacy snapshot must produce empty cursor")

	require.Len(t, session.Diffs(), numInferences, "legacy recovery must load full diff history into sess.diffs")

	_, snapData, err := store.LoadSnapshot("escrow-1")
	require.NoError(t, err)
	var blob sessionSnapshot
	require.NoError(t, json.Unmarshal(snapData, &blob))
	require.NotNil(t, blob.State, "snapshot must be upgraded to wrapper format on legacy recovery")
}

// legacyMetaWrapper wraps a Storage and forces meta.Version to "" for a
// specific escrow, simulating a corrupt or pre-versioning row. GetSessionMeta
// on real backends rejects empty stored versions; this wrapper is only used
// to exercise RecoverSession's boundVersion fallback when meta.Version is "".
type legacyMetaWrapper struct {
	storage.Storage
	legacyEscrow string
}

func (w *legacyMetaWrapper) GetSessionMeta(escrowID string) (*storage.SessionMeta, error) {
	meta, err := w.Storage.GetSessionMeta(escrowID)
	if err != nil {
		return nil, err
	}
	if escrowID == w.legacyEscrow {
		meta.Version = ""
	}
	return meta, nil
}

// TestRecoverSession_LegacyEmptyMetaVersion locks in the legacy bridge in
// RecoverSession: when storage returns meta.Version == "" (a pre-versioning
// row), recovery must succeed by falling back to the caller's boundVersion
// and the resulting state machine must be stamped with that bound value so
// the next settlement payload reports the running binary's composition tag.
func TestRecoverSession_LegacyEmptyMetaVersion(t *testing.T) {
	store := newTestStore(t)
	numHosts := 3
	numInferences := 3

	group, hosts, user := setupRecoverableSession(t, numHosts, numInferences, store)

	legacy := &legacyMetaWrapper{Storage: store, legacyEscrow: "escrow-1"}

	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(10))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	session, recSM, err := RecoverSession(legacy, user, verifier, "escrow-1",
		testutil.RuntimeTestVersion, group, clients)
	require.NoError(t, err, "recovery must bridge empty stored Version to boundVersion")
	require.Equal(t, uint64(numInferences), session.Nonce())

	exported := recSM.ExportState()
	require.NotNil(t, exported)
	require.Equal(t, testutil.RuntimeTestVersion, exported.StateRootAndProtocolVersion,
		"recovered state machine uses the session protocol version")
}

// TestRecoverSession_EmptyVersionRejected requires a version from storage or caller.
func TestRecoverSession_EmptyVersionRejected(t *testing.T) {
	store := newTestStore(t)
	numHosts := 3

	group, hosts, user := setupRecoverableSession(t, numHosts, 1, store)
	legacy := &legacyMetaWrapper{Storage: store, legacyEscrow: "escrow-1"}

	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(10))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	_, _, err := RecoverSession(legacy, user, verifier, "escrow-1", "", group, clients)
	require.Error(t, err)
	require.Contains(t, err.Error(), "session version required")
}

// validateDiffRange proves persisted-history integrity before replay. Both a
// gap in the middle and a missing tail (diffs end before latest_nonce, which
// would otherwise recover silently behind the hosts) must carry the
// deactivate-and-skip sentinel; a contiguous range must pass.
func TestValidateDiffRange(t *testing.T) {
	recs := func(nonces ...uint64) []types.DiffRecord {
		out := make([]types.DiffRecord, len(nonces))
		for i, n := range nonces {
			out[i] = types.DiffRecord{Diff: types.Diff{Nonce: n}}
		}
		return out
	}

	require.NoError(t, validateDiffRange(recs(1, 2, 3), 1, 3))
	require.NoError(t, validateDiffRange(recs(7, 8), 7, 8))
	require.NoError(t, validateDiffRange(nil, 5, 4), "empty range after snapshot")

	// Mid-range gap: the escrow 32269 shape (1..6 stored, then 151).
	err := validateDiffRange(recs(1, 2, 3, 4, 5, 6, 151, 152), 1, 152)
	require.ErrorIs(t, err, ErrLocalStateUnrecoverable)
	require.ErrorContains(t, err, "missing nonce 7, next stored nonce is 151")

	// Missing tail: latest_nonce points past the stored diffs.
	err = validateDiffRange(recs(1, 2, 3, 4, 5, 6), 1, 152)
	require.ErrorIs(t, err, ErrLocalStateUnrecoverable)
	require.ErrorContains(t, err, "missing trailing nonces 7..152")

	// Missing head right after a snapshot.
	err = validateDiffRange(recs(12), 11, 12)
	require.ErrorIs(t, err, ErrLocalStateUnrecoverable)
	require.ErrorContains(t, err, "missing nonce 11")
}

// TestRecoverSession_BackfillGapUnrecoverable verifies a missing diff inside
// the required pre-snapshot backfill range [min host cursor+1, snapNonce] is
// a proven integrity failure. The snapshot here is current (nothing to
// replay), so without backfill validation recovery returns success with a
// non-contiguous sess.diffs and the stranded host rejects catch-up forever
// with "invalid nonce: must be sequential".
func TestRecoverSession_BackfillGapUnrecoverable(t *testing.T) {
	store := newTestStore(t)
	numHosts := 3

	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))
	// Diff 3 is lost; latest_nonce lands on 4.
	for _, n := range []uint64{1, 2, 4} {
		require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{Diff: types.Diff{Nonce: n}}))
	}
	// Snapshot is current at 4, so nothing is replayed. Host 0 is stranded at
	// nonce 2 and needs the backfill range 3..4, which has a hole.
	sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	saveSnapshot(store, sm, "escrow-1", 4, map[int]uint64{0: 2, 1: 4, 2: 4})

	_, _, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group,
		buildRecoveryClients(t, hosts, group, user))

	require.ErrorIs(t, err, ErrLocalStateUnrecoverable)
	require.ErrorContains(t, err, "backfill diffs 3..4")
	require.ErrorContains(t, err, "missing nonce 3, next stored nonce is 4")
}

// A snapshot ahead of latest_nonce is ignored and recovery replays from 1.
// The backfill validation must not run against the ignored snapshot's nonce:
// contiguous history through latest_nonce is fully recoverable and must not
// be classified as missing a tail.
func TestRecoverSession_IgnoredFutureSnapshotRecovers(t *testing.T) {
	store := newTestStore(t)
	numHosts := 3

	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))
	for _, n := range []uint64{1, 2, 3} {
		require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{Diff: types.Diff{Nonce: n}}))
	}
	// Snapshot claims nonce 5 while latest_nonce is 3 (e.g. session row lost
	// a write the async snapshot kept).
	sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	saveSnapshot(store, sm, "escrow-1", 5, map[int]uint64{0: 5, 1: 5, 2: 5})

	session, _, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group,
		buildRecoveryClients(t, hosts, group, user))

	require.NoError(t, err)
	require.Equal(t, uint64(3), session.Nonce())
	require.Len(t, session.Diffs(), 3)
}

// The production failure this guards: a gateway built after the min_tokens floor could not start at
// all, because recovery replays diffs an earlier build wrote and one of them reserved a single token.
// "create runtimes: runtime 51153: create session: recover session: replay nonce 1003:
// max_tokens below min_tokens floor: max_tokens 1, floor 64".
func TestRecoverSession_ReplaysADiffWrittenBeforeTheMinTokensFloor(t *testing.T) {
	store := newTestStore(t)
	group, hosts, user := setupRecoverableSession(t, 2, 0, store)
	verifier := signing.NewSecp256k1Verifier()
	config := testutil.DefaultConfig(len(hosts))

	sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	belowFloor := []*types.DevshardTx{{Tx: &types.DevshardTx_StartInference{
		StartInference: &types.MsgStartInference{
			InferenceId: 1,
			PromptHash:  testutil.TestPromptHash[:],
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   1,
			StartedAt:   1000,
		},
	}}}
	root, err := sm.ApplyLocalPersisted(1, belowFloor)
	require.NoError(t, err, "seeding the diff an earlier build would have written")
	require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{
		Diff:      testutil.SignDiffWithRoot(t, user, "escrow-1", 1, belowFloor, root),
		StateHash: root,
	}))

	clients := make([]HostClient, len(hosts))
	session, recovered, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group, clients)

	require.NoError(t, err, "recovery must replay history the running policy would no longer create")
	require.NotNil(t, session)
	require.Equal(t, types.StatusPending, recovered.SnapshotState().Inferences[1].Status)
}

func TestDecodeSnapshot_HeightSyncFloor(t *testing.T) {
	blob, err := json.Marshal(sessionSnapshot{
		State: &types.EscrowState{EscrowID: "escrow-1", LatestNonce: 3},
		HeightSyncFloor: &types.FloorIndexProto{
			Truncated: true,
			Entries: []*types.FloorIndexEntryProto{{
				Nonce:  3,
				Height: 50,
				Hash:   []byte{0xaa},
				Author: 1,
			}},
		},
	})
	require.NoError(t, err)

	st, _, _, _, floor, err := decodeSnapshot(blob)
	require.NoError(t, err)
	require.Equal(t, "escrow-1", st.EscrowID)
	require.NotNil(t, floor)
	require.True(t, floor.Truncated)
	require.Equal(t, uint64(50), floor.Entries[0].Height)
	require.Equal(t, []byte{0xaa}, floor.Entries[0].Hash)

	legacy, err := json.Marshal(types.EscrowState{EscrowID: "escrow-2"})
	require.NoError(t, err)
	st2, _, _, _, floor2, err := decodeSnapshot(legacy)
	require.NoError(t, err)
	require.Equal(t, "escrow-2", st2.EscrowID)
	require.Nil(t, floor2)
}
