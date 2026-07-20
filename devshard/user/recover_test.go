package user

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard"
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
