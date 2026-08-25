package user

import (
	"context"
	"encoding/json"
	"path/filepath"
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
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
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}
	resp, err := session.SendInference(ctx, params)
	require.NoError(t, err)
	require.Equal(t, uint64(numInferences+1), resp.Nonce)
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
	nonceAfterTurn2 := recovered.Nonce()

	require.NoError(t, recovered.ProcessResponse(span[0].hostIdx, &host.HostResponse{
		Nonce:   recovered.Nonce(),
		Mempool: []*types.DevshardTx{{Tx: &types.DevshardTx_HeightAck{HeightAck: oldAck}}},
	}, recovered.Nonce()))
	require.Empty(t, heightAcksInTxs(recovered.PendingTxs()),
		"the late old ack was already recovered through catch-up and must not be queued again")
	require.Equal(t, nonceAfterTurn2, recovered.Nonce())
	rec2After := recovered.HeartbeatTurnTracker().Record(2)
	require.Equal(t, rec2Before.Acks, rec2After.Acks,
		"a late turn_seq=1 ack must not count as an ack for turn_seq=2")
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

	recovered, _, err := RecoverSession(store, user, signing.NewSecp256k1Verifier(),
		"escrow-1", testutil.RuntimeTestVersion, group,
		recoveryHeartbeatClients(t, group, hosts, user))
	require.NoError(t, err)
	t.Cleanup(func() { _ = recovered.Close() })
	recovered.SetHeightSyncCadence(10, uint64(len(hosts)))
	recovered.SetObservedHeight(func() (uint64, []byte, bool) { return height, []byte{0xaa}, true })

	require.Equal(t, uint64(4), recovered.Nonce())
	require.Equal(t, heightsync.TurnComplete, recovered.HeartbeatTurnTracker().Record(1).State)
	require.NoError(t, recovered.sendCatchUp(ctx, ackHostIdx))
	require.Equal(t, ackDiff.Nonce, recovered.hostSyncNonce[ackHostIdx])
	require.Equal(t, uint64(4), recovered.Nonce(), "catch-up must not compose a duplicate ack-flush diff")
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

func syncVectorStatuses(vec []*types.SyncVectorEntry) map[int]types.AckStatus {
	out := make(map[int]types.AckStatus, len(vec))
	for _, ent := range vec {
		out[int(ent.SlotId)] = ent.Status
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
		testutil.TestPromptHash[:], "llama", 100, 50, 1000, 2000)
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
