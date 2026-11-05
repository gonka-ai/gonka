package user

import (
	"context"
	"errors"
	"testing"
	"time"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// Finalize must flush a ready Finish when there is no later Start to carry it.
func TestCompletionFinalizeConsumesReadyFinish(t *testing.T) {
	session, _, _, _ := setupArmedSession(t, 3, 100000, 10)
	t.Cleanup(func() { _ = session.Close() })
	journal := newMemoryCompletionStore()
	session.SetInferenceCompletionStore(journal)
	_, err := session.SendInference(context.Background(), completionParams())
	require.NoError(t, err)
	nonce := session.Nonce()
	rec, ok := session.StateMachine().Inference(nonce)
	require.True(t, ok)
	require.Contains(t, []types.InferenceStatus{types.StatusPending, types.StatusStarted}, rec.Status)
	session.mu.Lock()
	ready := session.pendingFinishReadyLocked(nonce)
	session.mu.Unlock()
	require.True(t, ready, "precondition: a verified Finish is already queued and can apply")
	t.Logf("before Finalize: nonce=%d inference=%d status=%v ready_finish=%t", session.Nonce(), nonce, rec.Status, ready)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	err = session.Finalize(ctx)
	t.Logf("after Finalize: duration=%s nonce=%d phase=%v error=%v", time.Since(started), session.Nonce(), session.StateMachine().Phase(), err)
	require.NoError(t, err, "Finalize must consume an already verified completed result")
	require.Equal(t, types.PhaseSettlement, session.StateMachine().Phase())
	entries, err := journal.ListInferenceCompletions("escrow-1")
	require.NoError(t, err)
	require.Empty(t, entries)
}

func readyCompletionSession(t *testing.T, durable bool) (*Session, *memoryCompletionStore, *storage.SQLite) {
	t.Helper()
	signers := make([]*signing.Secp256k1Signer, 3)
	for i := range signers {
		signers[i] = testutil.MustGenerateKey(t)
	}
	config := testutil.DefaultConfig(len(signers))
	config.CreateDevshardFee = 10
	config.FeePerNonce = 7
	s, creator, _ := armedSessionFor(t, testutil.MakeGroup(signers), signers, config, 100000, 10)
	journal := newMemoryCompletionStore()
	var db *storage.SQLite
	if durable {
		db = newTestStore(t)
		require.NoError(t, db.CreateSession(storage.CreateSessionParams{EscrowID: s.escrowID, Version: testutil.RuntimeTestVersion, CreatorAddr: creator.Address(), Config: config, Group: s.group, InitialBalance: 100000}))
		for _, diff := range s.Diffs() {
			require.NoError(t, db.AppendDiff(s.escrowID, types.DiffRecord{Diff: diff, StateHash: diff.PostStateRoot}))
		}
		s.store = db
	}
	s.SetInferenceCompletionStore(journal)
	t.Cleanup(func() { _ = s.Close() })
	_, err := s.SendInference(context.Background(), completionParams())
	require.NoError(t, err)
	s.mu.Lock()
	ready := s.pendingFinishReadyLocked(s.nonce)
	s.mu.Unlock()
	require.True(t, ready)
	return s, journal, db
}

func TestCompletionFinalizeCombinesReadyFinish(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "sqlite"}[durable], func(t *testing.T) {
			s, journal, db := readyCompletionSession(t, durable)
			before := s.sm.SnapshotState()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			require.NoError(t, s.Finalize(ctx))
			require.Equal(t, before.LatestNonce+1, s.sm.FinalizeNonce(), "ready results must ride the first finalization diff")
			require.Equal(t, before.Fees, s.sm.SnapshotState().Fees, "Finalize must not create an extra active nonce")
			require.Equal(t, types.PhaseSettlement, s.sm.Phase())
			require.True(t, s.hasQuorum(s.Nonce(), s.sm.QuorumThreshold()))
			entries, err := s.completionStore.ListInferenceCompletions(s.escrowID)
			require.NoError(t, err)
			require.Empty(t, entries)
			if db != nil {
				diffs, err := db.GetDiffs(s.escrowID, before.LatestNonce+1, before.LatestNonce+1)
				require.NoError(t, err)
				require.Len(t, diffs, 1)
				var finish, finalize bool
				for _, tx := range diffs[0].Txs {
					if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == before.LatestNonce {
						finish = true
					}
					finalize = finalize || tx.GetFinalizeRound() != nil
				}
				require.True(t, finish)
				require.True(t, finalize)
				entries, err := journal.ListInferenceCompletions(s.escrowID)
				require.NoError(t, err)
				require.Empty(t, entries)
			}
		})
	}
}

func TestCompletionFinalizeRejectsDroppedFinishBeforeCommit(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "sqlite"}[durable], func(t *testing.T) {
			s, _, db := readyCompletionSession(t, durable)
			before := s.sm.SnapshotState()
			s.mu.Lock()
			for i, tx := range s.pendingTxs {
				if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == before.LatestNonce {
					s.pendingTxs[i] = proto.Clone(tx).(*types.DevshardTx)
					s.pendingTxs[i].GetFinishInference().ProposerSig = nil
				}
			}
			s.mu.Unlock()
			finalize := &types.DevshardTx{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}}
			err := s.finalizeDiffRound(context.Background(), []*types.DevshardTx{finalize})
			require.ErrorIs(t, err, ErrInferenceCompletionPending)
			require.Equal(t, before.LatestNonce, s.Nonce())
			require.Equal(t, types.PhaseActive, s.sm.Phase())
			require.Equal(t, before.Fees, s.sm.SnapshotState().Fees)
			entries, err := s.completionStore.ListInferenceCompletions(s.escrowID)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			if db != nil {
				diffs, err := db.GetDiffs(s.escrowID, before.LatestNonce+1, before.LatestNonce+1)
				require.NoError(t, err)
				require.Empty(t, diffs)
			}
		})
	}
}

func TestCompletionFinalizeFailedCommitKeepsReadyFinish(t *testing.T) {
	s, journal, db := readyCompletionSession(t, true)
	before := s.sm.SnapshotState()
	s.store = rejectedCompletionDiff{db}
	require.ErrorIs(t, s.Finalize(context.Background()), storage.ErrDiffFork)
	require.Equal(t, before.LatestNonce, s.Nonce())
	require.Equal(t, types.PhaseActive, s.sm.Phase())
	require.Equal(t, before.Fees, s.sm.SnapshotState().Fees)
	entries, err := journal.ListInferenceCompletions(s.escrowID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, before.LatestNonce, entries[0].Nonce)
	s.store = db
	require.NoError(t, s.Finalize(context.Background()))
	require.Equal(t, before.LatestNonce+1, s.sm.FinalizeNonce())
	entries, err = journal.ListInferenceCompletions(s.escrowID)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestCompletionFinalizeRetriesLegacyJournalCleanup(t *testing.T) {
	s, journal, _ := readyCompletionSession(t, false)
	journal.deleteErr = errors.New("temporary completion cleanup failure")
	before := s.Nonce()
	require.ErrorIs(t, s.Finalize(context.Background()), ErrInferenceCompletionPending)
	require.Equal(t, before+1, s.Nonce())
	require.Equal(t, types.PhaseFinalizing, s.sm.Phase())
	journal.deleteErr = nil
	require.NoError(t, s.Finalize(context.Background()))
	require.Equal(t, types.PhaseSettlement, s.sm.Phase())
	entries, err := journal.ListInferenceCompletions(s.escrowID)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestCompletionFinalizeRejectsPartialCompletions(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "sqlite"}[durable], func(t *testing.T) {
			s, _, _ := readyCompletionSession(t, durable)
			readyNonce := s.Nonce()
			s.pinPendingFinish(readyNonce)
			_, err := s.PrepareInference(context.Background(), completionParams())
			s.unpinPendingFinish(readyNonce)
			require.NoError(t, err)
			before := s.Nonce()
			entries, err := s.completionStore.ListInferenceCompletions(s.escrowID)
			require.NoError(t, err)
			require.Len(t, entries, 2)
			finalize := &types.DevshardTx{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}}
			err = s.finalizeDiffRound(context.Background(), []*types.DevshardTx{finalize})
			require.ErrorIs(t, err, ErrInferenceCompletionPending)
			require.Equal(t, before, s.Nonce())
			require.Equal(t, types.PhaseActive, s.sm.Phase())
			rec, ok := s.sm.Inference(readyNonce)
			require.True(t, ok)
			require.False(t, finishAppliedStatus(rec.Status), "the rejected preview must undo even the valid Finish")
		})
	}
}

// This base keeps completion obligations in a separate store. The durable
// session diff must still fail before advancing or clearing an obligation.
type rejectedCompletionDiff struct{ storage.Storage }

func (rejectedCompletionDiff) AppendDiff(string, types.DiffRecord) error { return storage.ErrDiffFork }
