package state

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

// TestCommittedEntriesHashMatchesMarshalPath drives every live-set write
// (start, confirm, finish, valid and invalid validation, validation votes,
// timeout, auto-seal, settlement drain) and checks that the
// stored protobuf bytes hash to the same state root as a fresh marshal.
// Missing cached entries fall back to the original marshal path.
func TestCommittedEntriesHashMatchesMarshalPath(t *testing.T) {
	const (
		escrowID    = "escrow-hash"
		graceNonces = 6
		nHosts      = 5
	)
	hosts := make([]*signing.Secp256k1Signer, nHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		TokenPrice:                1,
		VoteThreshold:             1,
		FeePerNonce:               0,
		ExecutionTimeout:          0,
		InferenceSealGraceSeconds: 3600,
		InferenceSealGraceNonces:  graceNonces,
		AutoSealEveryNNonces:      1,
	}
	sm, err := NewStateMachine(escrowID, config, group, 1_000_000, user.Address(),
		signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, escrowID, user.Address(), config, group, 1_000_000))
	require.NoError(t, err)

	apply := func(nonce uint64, txs ...*types.DevshardTx) {
		t.Helper()
		root, err := sm.ApplyLocal(nonce, txs)
		require.NoError(t, err)
		assertCommittedMatchesMarshalPath(t, sm)
		got, err := sm.ComputeStateRoot()
		require.NoError(t, err)
		require.Equal(t, root, got)
	}
	empty := func(nonce uint64) { t.Helper(); apply(nonce) }

	// Finished record. The clock gate is an hour, so auto-seal leaves it live.
	apply(1, startTx(1))
	apply(2, confirmTx(t, hosts, escrowID, 1))
	apply(3, finishTx(t, hosts, escrowID, 1))

	// Valid validation stays Finished and rewrites the stored bytes.
	apply(4, startTx(4))
	apply(5, confirmTx(t, hosts, escrowID, 4))
	apply(6, finishTx(t, hosts, escrowID, 4))
	apply(7, validationTx(t, hosts, escrowID, 4, 0, true), validationTx(t, hosts, escrowID, 4, 0, true))
	rec := sm.SnapshotState().Inferences[4]
	require.Equal(t, types.StatusFinished, rec.Status)
	require.Equal(t, uint32(1), rec.VotesValid)

	// Invalid validation, then enough valid votes to reach Validated.
	// Executor for id 8 is slot 3; slot 4 challenges; slots 0 and 1 vote valid.
	apply(8, startTx(8))
	apply(9, confirmTx(t, hosts, escrowID, 8))
	apply(10, finishTx(t, hosts, escrowID, 8))
	apply(11, validationTx(t, hosts, escrowID, 8, 4, false))
	require.Equal(t, types.StatusChallenged, sm.SnapshotState().Inferences[8].Status)
	apply(12, voteTx(t, hosts, escrowID, 8, 0, true))
	require.Equal(t, types.StatusChallenged, sm.SnapshotState().Inferences[8].Status)
	apply(13, voteTx(t, hosts, escrowID, 8, 1, true))
	require.Equal(t, types.StatusValidated, sm.SnapshotState().Inferences[8].Status)

	// Nonce gate for id 8 is 14. The start in this diff lands first; auto-seal then drops 8.
	apply(14, startTx(14))
	require.NotContains(t, liveIDs(sm), uint64(8))
	require.Contains(t, sm.ExportSealedNonces(), uint64(8))

	apply(15, confirmTx(t, hosts, escrowID, 14))
	apply(16, finishTx(t, hosts, escrowID, 14))
	// Executor for id 14 is slot 4. Slot 0 challenges; slot 1 votes invalid.
	apply(17, validationTx(t, hosts, escrowID, 14, 0, false))
	apply(18, voteTx(t, hosts, escrowID, 14, 1, false))
	require.Equal(t, types.StatusInvalidated, sm.SnapshotState().Inferences[14].Status)

	apply(19, startTx(19))
	// Nonce gate for id 14 is 20.
	apply(20, confirmTx(t, hosts, escrowID, 19))
	require.NotContains(t, liveIDs(sm), uint64(14))
	require.Contains(t, sm.ExportSealedNonces(), uint64(14))

	apply(21, timeoutTx(t, hosts, escrowID, 19, types.TimeoutReason_TIMEOUT_REASON_EXECUTION))
	require.Equal(t, types.StatusTimedOut, sm.SnapshotState().Inferences[19].Status)

	apply(22, startTx(22))
	apply(23, confirmTx(t, hosts, escrowID, 22))
	apply(24, finishTx(t, hosts, escrowID, 22))
	// Nonce gate for id 19 is 25, so this diff seals 19. id 22 stays Finished.
	empty(25)
	require.NotContains(t, liveIDs(sm), uint64(19))
	require.Equal(t, types.StatusFinished, sm.SnapshotState().Inferences[22].Status)

	empty(26)
	empty(27)
	empty(28)
	require.Equal(t, []uint64{1, 4, 22}, liveIDs(sm))

	exported := sm.ExportCommittedEntries()
	sm.RestoreCommittedEntries(exported)
	assertCommittedMatchesMarshalPath(t, sm)
	sm.RestoreCommittedEntries(nil)
	assertCommittedMatchesMarshalPath(t, sm)

	sm.mu.Lock()
	delete(sm.committedEntries, 1)
	sm.mu.Unlock()
	got, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	st := sm.SnapshotState()
	rest, err := ComputeRestHashV2(st.Balance, sealedAccBytes32(st.SealedAcc), st.Inferences, st.WarmKeys)
	require.NoError(t, err)
	hostsHash, err := computeHostStatsHash(st.HostStats)
	require.NoError(t, err)
	require.Equal(t, ComputeStateRootFromRestHash(hostsHash, rest, st.Fees, st.Phase, st.StateRootAndProtocolVersion), got)

	sm.RestoreCommittedEntries(nil)
	assertCommittedMatchesMarshalPath(t, sm)

	apply(29, txFinalize())
	require.Equal(t, types.PhaseFinalizing, sm.Phase())
	for nonce := uint64(30); nonce <= 33; nonce++ {
		empty(nonce)
		require.Equal(t, types.PhaseFinalizing, sm.Phase())
		require.Equal(t, []uint64{1, 4, 22}, liveIDs(sm))
	}
	// LatestNonce >= FinalizeNonce+len(group) drains every remaining live record.
	empty(34)
	require.Equal(t, types.PhaseSettlement, sm.Phase())
	require.Empty(t, liveIDs(sm))
	require.Empty(t, sm.ExportCommittedEntries())
}

func assertCommittedMatchesMarshalPath(t *testing.T, sm *StateMachine) {
	t.Helper()
	st := sm.SnapshotState()
	entries := sm.ExportCommittedEntries()
	require.Equal(t, len(st.Inferences), len(entries))
	for id, rec := range st.Inferences {
		got, ok := entries[id]
		require.True(t, ok, "committed entry missing for live id %d", id)
		want, err := marshalInferenceEntry(id, rec)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	fromStructs, err := computeInferencesHash(st.Inferences)
	require.NoError(t, err)
	folded := computeInferencesHashFromEntries(entries)
	require.Equal(t, fromStructs, folded[:])

	rest := restHashV2FromLiveHash(st.Balance, sealedAccBytes32(st.SealedAcc), fromStructs, st.WarmKeys)
	hostHash, err := computeHostStatsHash(st.HostStats)
	require.NoError(t, err)
	want := ComputeStateRootFromRestHash(hostHash, rest, st.Fees, st.Phase, st.StateRootAndProtocolVersion)
	got, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func liveIDs(sm *StateMachine) []uint64 {
	st := sm.SnapshotState()
	ids := make([]uint64, 0, len(st.Inferences))
	for id := range st.Inferences {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func startTx(id uint64) *types.DevshardTx {
	return txStart(&types.MsgStartInference{
		InferenceId: id,
		PromptHash:  []byte("prompt"),
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   1000,
	})
}

func confirmTx(t *testing.T, hosts []*signing.Secp256k1Signer, escrowID string, id uint64) *types.DevshardTx {
	t.Helper()
	slot := uint32(id % uint64(len(hosts)))
	sig := testutil.SignExecutorReceipt(t, hosts[slot], escrowID, id, []byte("prompt"), "llama", 100, 50, 1000, 1000)
	return txConfirm(&types.MsgConfirmStart{
		InferenceId: id,
		ExecutorSig: sig,
		ConfirmedAt: 1000,
	})
}

func finishTx(t *testing.T, hosts []*signing.Secp256k1Signer, escrowID string, id uint64) *types.DevshardTx {
	t.Helper()
	slot := uint32(id % uint64(len(hosts)))
	msg := &types.MsgFinishInference{
		InferenceId:  id,
		ResponseHash: []byte("response"),
		InputTokens:  80,
		OutputTokens: 40,
		ExecutorSlot: slot,
		EscrowId:     escrowID,
	}
	msg.ProposerSig = testutil.SignProposerTx(t, hosts[slot], msg)
	return txFinish(msg)
}

func validationTx(t *testing.T, hosts []*signing.Secp256k1Signer, escrowID string, id uint64, slot uint32, valid bool) *types.DevshardTx {
	t.Helper()
	msg := &types.MsgValidation{
		InferenceId:   id,
		ValidatorSlot: slot,
		Valid:         valid,
		EscrowId:      escrowID,
	}
	msg.ProposerSig = testutil.SignProposerTx(t, hosts[slot], msg)
	return txValidation(msg)
}

func voteTx(t *testing.T, hosts []*signing.Secp256k1Signer, escrowID string, id uint64, slot uint32, voteValid bool) *types.DevshardTx {
	t.Helper()
	msg := &types.MsgValidationVote{
		InferenceId: id,
		VoterSlot:   slot,
		VoteValid:   voteValid,
		EscrowId:    escrowID,
	}
	msg.ProposerSig = testutil.SignProposerTx(t, hosts[slot], msg)
	return txVote(msg)
}

func timeoutTx(t *testing.T, hosts []*signing.Secp256k1Signer, escrowID string, id uint64, reason types.TimeoutReason) *types.DevshardTx {
	t.Helper()
	votes := make([]*types.TimeoutVote, 0, 2)
	for _, slot := range []uint32{0, 1} {
		v := testutil.SignTimeoutVote(t, hosts[slot], escrowID, id, reason, true)
		v.VoterSlot = slot
		votes = append(votes, v)
	}
	return txTimeout(&types.MsgTimeoutInference{
		InferenceId: id,
		Reason:      reason,
		Votes:       votes,
	})
}

// Preserve the release behavior when a manual seal cannot write observability.
func TestSealInferenceObsFailurePreservesV41Behavior(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	mem := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000)
	sm, err := NewStateMachine("escrow-1", config, group, 10000, user.Address(),
		signing.NewSecp256k1Verifier(), &failingObsInsertStore{Memory: mem})
	require.NoError(t, err)
	_, err = sm.ApplyLocal(1, []*types.DevshardTx{startTx(1)})
	require.NoError(t, err)

	require.ErrorContains(t, sm.SealInference(1), "injected obs insert failure")
	require.Equal(t, []uint64{1}, liveIDs(sm))
	require.Empty(t, sm.ExportCommittedEntries())

	_, err = sm.ApplyLocal(2, []*types.DevshardTx{startTx(2)})
	require.NoError(t, err)
	root, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	st := sm.SnapshotState()
	rest, err := ComputeRestHashV2(st.Balance, sealedAccBytes32(st.SealedAcc), st.Inferences, st.WarmKeys)
	require.NoError(t, err)
	hostHash, err := computeHostStatsHash(st.HostStats)
	require.NoError(t, err)
	require.Equal(t, ComputeStateRootFromRestHash(hostHash, rest, st.Fees, st.Phase, st.StateRootAndProtocolVersion), root)
}
