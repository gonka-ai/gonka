package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
	"devshard/user"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestLongContentStillResolvesThroughProtocolTimeout(t *testing.T) {
	for _, cleanup := range []string{"winner", "failed"} {
		for _, receipt := range []bool{false, true} {
			t.Run(cleanup+map[bool]string{false: "_refused", true: "_execution"}[receipt], func(t *testing.T) {
				env := setupTestProxy(t, 16, nil, true)
				store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
				require.NoError(t, err)
				defer store.Close()
				env.session.SetInferenceCompletionStore(store)
				params := defaultParams()
				old := time.Now().Add(-10 * time.Minute)
				params.StartedAt = old.Unix()
				initial := env.sm.SnapshotState().Balance
				p, err := env.session.PrepareInference(params)
				require.NoError(t, err)
				if receipt {
					sig := testutil.SignExecutorReceipt(t, env.verifiers[p.HostIdx()].signer, "escrow-proxy", p.Nonce(), testutil.TestPromptHash[:], params.Model, params.InputLength, params.MaxTokens, params.StartedAt, old.Unix())
					require.NoError(t, env.session.ProcessResponse(p.HostIdx(), &host.HostResponse{Receipt: sig, ConfirmedAt: old.Unix()}, p.Nonce()))
					require.NoError(t, env.session.SendPendingDiff(context.Background()))
				}
				inf := &inflight{hostIdx: p.HostIdx(), hostID: env.session.HostLabel(p.HostIdx()), nonce: p.Nonce(), escrowID: "escrow-proxy", sendTime: old, resp: &host.HostResponse{Nonce: p.Nonce()}, done: make(chan struct{}), contentSource: "delta.content"}
				inf.outputChunks.Store(1)
				inf.contentChunks.Store(1)
				close(inf.done)
				require.True(t, longResponsePerfExempt(inf, env.session))
				if cleanup == "winner" {
					require.True(t, deliveredWholeAnswer(inf))
					require.NoError(t, env.proxy.redundancy.finishRaceOutcome(context.Background(), []*inflight{inf}, params, Decision{Reason: "test"}, p.Nonce(), raceFinishOptions{}))
					env.proxy.redundancy.waitRaceCleanups()
				} else {
					inf.err = errors.New("stream failed")
					env.proxy.redundancy.voteTimeoutsForFailedRequest(context.Background(), []*inflight{inf}, params)
				}
				rec, ok := env.sm.Inference(p.Nonce())
				require.True(t, ok)
				require.Equal(t, types.StatusTimedOut, rec.Status)
				state := env.sm.SnapshotState()
				require.Equal(t, initial, state.Balance)
				require.Zero(t, state.HostStats[rec.ExecutorSlot].Cost)
				require.Equal(t, uint32(1), state.HostStats[rec.ExecutorSlot].Missed)
				require.NoError(t, env.session.Finalize(context.Background()))
				require.Equal(t, types.PhaseSettlement, env.sm.Phase())
				require.True(t, env.session.HasQuorumAt(env.sm.LatestNonce()))
				state = env.sm.SnapshotState()
				require.Zero(t, state.HostStats[rec.ExecutorSlot].Cost)
				require.Equal(t, uint32(1), state.HostStats[rec.ExecutorSlot].Missed)
				entries, err := store.ListInferenceCompletions("escrow-proxy")
				require.NoError(t, err)
				require.Empty(t, entries)
			})
		}
	}
}

func TestCompletionStoreSurvivesRestartAndRejectsConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := NewGatewayStore(path)
	require.NoError(t, err)
	entry := user.InferenceCompletion{Nonce: 7, PreparedAt: time.Now().UnixNano(), Payload: host.InferencePayload{Prompt: testutil.TestPrompt, Model: "llama", StartedAt: 1000}}
	require.NoError(t, store.SaveInferenceCompletion("escrow", entry))
	require.NoError(t, store.Close())
	store, err = NewGatewayStore(path)
	require.NoError(t, err)
	defer store.Close()
	entries, err := store.ListInferenceCompletions("escrow")
	require.NoError(t, err)
	require.Equal(t, []user.InferenceCompletion{entry}, entries)
	conflict := entry
	conflict.Payload.Model = "different"
	require.ErrorIs(t, store.SaveInferenceCompletion("escrow", conflict), user.ErrInferenceCompletionConflict)
	later := entry
	later.PreparedAt += 100
	require.NoError(t, store.SaveInferenceCompletion("escrow", later))
	entries, err = store.ListInferenceCompletions("escrow")
	require.NoError(t, err)
	require.Equal(t, entry.PreparedAt, entries[0].PreparedAt)
	var synchronous int
	require.NoError(t, store.db.QueryRow("PRAGMA synchronous").Scan(&synchronous))
	require.Equal(t, 2, synchronous)
	require.NoError(t, store.DeleteInferenceCompletion("escrow", 7))
	require.NoError(t, store.DeleteInferenceCompletion("escrow", 7))
	entries, err = store.ListInferenceCompletions("escrow")
	require.NoError(t, err)
	require.Empty(t, entries)
	_, err = store.db.Exec("INSERT INTO gateway_inference_completions (escrow_id,nonce,entry_json) VALUES ('escrow',8,'{corrupt}')")
	require.NoError(t, err)
	_, err = store.ListInferenceCompletions("escrow")
	require.Error(t, err)
}

func completionRestartFixture(t *testing.T) (*state.SettlementPayload, []types.SlotAssignment, string) {
	t.Helper()
	const escrowID = "42"
	signers := make([]*signing.Secp256k1Signer, 16)
	for i := range signers {
		signers[i] = testutil.MustGenerateKey(t)
	}
	creator := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(signers)
	verifier := signing.NewSecp256k1Verifier()
	config := testutil.DefaultConfig(16)
	config.RefusalTimeout = 1
	sessionPath := filepath.Join(t.TempDir(), "session")
	db, err := storage.NewSQLite(sessionPath)
	require.NoError(t, err)
	require.NoError(t, db.CreateSession(storage.CreateSessionParams{EscrowID: escrowID, Version: testutil.RuntimeTestVersion, CreatorAddr: creator.Address(), Config: config, Group: group, InitialBalance: 1000000}))
	clients := make([]user.HostClient, 16)
	voters := make([]*numericCompletionClient, 16)
	for i := range clients {
		sm := statetest.MustStateMachine(t, escrowID, config, group, 1000000, creator.Address(), verifier)
		h, err := host.NewHost(sm, signers[i], stub.NewInferenceEngine(), escrowID, group, nil, host.WithGrace(10))
		require.NoError(t, err)
		voters[i] = &numericCompletionClient{InProcessClient: &user.InProcessClient{Host: h}, signer: signers[i], slot: group[i].SlotID}
		clients[i] = voters[i]
	}
	sm := statetest.MustStateMachine(t, escrowID, config, group, 1000000, creator.Address(), verifier)
	session, err := user.NewSession(sm, creator, escrowID, group, clients, verifier, user.WithStorage(db))
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "gateway.db")
	journal, err := NewGatewayStore(path)
	require.NoError(t, err)
	session.SetInferenceCompletionStore(journal)
	params := defaultParams()
	params.StartedAt = time.Now().Add(-time.Hour).Unix()
	p, err := session.PrepareInference(params)
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, session.Finalize(canceled), user.ErrInferenceCompletionPending)
	require.Equal(t, types.PhaseActive, sm.Phase())
	require.NoError(t, journal.Close())
	require.NoError(t, db.Close())
	db, err = storage.NewSQLite(sessionPath)
	require.NoError(t, err)
	defer db.Close()
	journal, err = NewGatewayStore(path)
	require.NoError(t, err)
	defer journal.Close()
	recovered, recoveredSM, err := user.RecoverSession(db, creator, verifier, escrowID, testutil.RuntimeTestVersion, group, clients)
	require.NoError(t, err)
	recovered.SetInferenceCompletionStore(journal)
	for _, voter := range voters {
		voter.accept = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, recovered.Finalize(ctx))
	after := recoveredSM.SnapshotState()
	require.Equal(t, types.PhaseSettlement, after.Phase)
	require.Equal(t, uint64(1000000), after.Balance)
	require.Zero(t, after.HostStats[uint32(p.HostIdx())].Cost)
	require.Equal(t, uint32(1), after.HostStats[uint32(p.HostIdx())].Missed)
	entries, err := journal.ListInferenceCompletions(escrowID)
	require.NoError(t, err)
	require.Empty(t, entries)
	payload, err := state.BuildSettlement(escrowID, after, recovered.Signatures()[recovered.Nonce()], recovered.Nonce())
	require.NoError(t, err)
	_, err = state.VerifySettlement(*payload, group, verifier, after.WarmKeys)
	require.NoError(t, err)
	return payload, group, creator.Address()
}

type numericCompletionClient struct {
	*user.InProcessClient
	signer *signing.Secp256k1Signer
	slot   uint32
	accept bool
}

func (c *numericCompletionClient) VerifyTimeout(_ context.Context, id uint64, reason types.TimeoutReason, _ *host.InferencePayload, _ []types.Diff, _ host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	if !c.accept {
		return false, nil, 0, nil, "", nil
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(&types.TimeoutVoteContent{EscrowId: "42", InferenceId: id, Reason: reason, Accept: true})
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	sig, err := c.signer.Sign(data)
	return err == nil, sig, c.slot, nil, "", err
}
func (c *numericCompletionClient) VerifyErrorMiss(context.Context, uint64, []types.Diff, host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	return false, nil, 0, nil, "", nil
}

func TestCompletionRestartReconcilesBeforeSettlement(t *testing.T) { completionRestartFixture(t) }

func TestLongContentWithCanonicalFinishChargesActualCost(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	defer store.Close()
	env.session.SetInferenceCompletionStore(store)
	params := defaultParams()
	p, err := env.session.PrepareInference(params)
	require.NoError(t, err)
	resp, err := env.session.SendOnly(context.Background(), p, nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.session.ProcessResponse(p.HostIdx(), resp, p.Nonce()))
	require.NoError(t, env.session.SendPendingDiff(context.Background()))
	rec, ok := env.sm.Inference(p.Nonce())
	require.True(t, ok)
	require.Equal(t, types.StatusFinished, rec.Status)
	inf := &inflight{hostIdx: p.HostIdx(), nonce: p.Nonce(), sendTime: time.Now().Add(-10 * time.Minute), contentSource: "delta.content", resp: resp, done: make(chan struct{})}
	inf.outputChunks.Store(1)
	inf.contentChunks.Store(1)
	close(inf.done)
	env.proxy.redundancy.voteTimeoutsForFailedRequest(context.Background(), []*inflight{inf}, params)
	require.NoError(t, env.session.Finalize(context.Background()))
	stats := env.sm.SnapshotState().HostStats[rec.ExecutorSlot]
	require.Equal(t, rec.ActualCost, stats.Cost)
	require.LessOrEqual(t, stats.Cost, rec.ReservedCost)
	require.Zero(t, stats.Missed)
	entries, err := store.ListInferenceCompletions("escrow-proxy")
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestCompletionRehydratedSettlementKeepsPendingObligation(t *testing.T) {
	env := setupTestProxy(t, 3, nil, false)
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	defer store.Close()
	env.session.SetInferenceCompletionStore(store)
	params := defaultParams()
	params.StartedAt = time.Now().Add(-time.Hour).Unix()
	p, err := env.session.PrepareInference(params)
	require.NoError(t, err)
	cfg := RuntimeConfig{ID: "42", Model: "llama", StoragePath: filepath.Join(t.TempDir(), "escrow-42")}
	require.NoError(t, store.Initialize(GatewaySettings{DefaultModel: "llama"}, []GatewayDevshardState{{RuntimeConfig: cfg, Active: false, SettlementPending: true}}))
	g := NewGateway(nil, NewGatewayLimiter(0, 0), "llama")
	g.store = store
	oldBuilder := gatewayRuntimeBuilder
	defer func() { gatewayRuntimeBuilder = oldBuilder }()
	gatewayRuntimeBuilder = func(RuntimeConfig, runtimeBuildDeps) (*devshardRuntime, error) {
		return &devshardRuntime{id: "42", session: env.session, proxy: env.proxy}, nil
	}
	stubChainBridge(t, nil)
	env.session.SetInferenceCompletionStore(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = g.settleDevshardOnChain(ctx, "42", adminSettleEscrowRequest{PrivateKey: testutil.MustGenerateKey(t).PrivateKeyHex()})
	require.ErrorIs(t, err, user.ErrInferenceCompletionPending)
	require.Equal(t, types.PhaseActive, env.sm.Phase())
	rec, ok := env.sm.Inference(p.Nonce())
	require.True(t, ok)
	require.Equal(t, types.StatusPending, rec.Status)
	record, ok, err := store.GetDevshard("42")
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, record.SettlementPending)
	entries, err := store.ListInferenceCompletions("escrow-proxy")
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
