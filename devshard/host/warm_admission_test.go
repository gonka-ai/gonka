package host

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
	"github.com/stretchr/testify/require"
)

// The sender can select a genuine old checkpoint, but creates the work only
// after revocation. Historical deterministic validation intentionally accepts
// it; the host must reject it at the network admission boundary, before write.
func retiredWork(t *testing.T, lag int, expiry bool) (*warmRecoveryFixture, types.Diff, types.Diff) {
	t.Helper()
	f := newWarmRecoveryFixture(t)
	cfg := f.sm.SnapshotState().Config
	old, err := f.sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	expiryTime := time.Unix(0, old.GetMainnetHeight().TimeUnixNano).Add(time.Second)
	f.sm.SetWarmKeyHeightChecker(func(c, w string, id uint64, h int64, at time.Time) (bool, error) {
		live := h < cfg.MainnetAnchorHeight+1
		if expiry {
			live = !at.After(expiryTime)
		}
		return c == f.cold[1].Address() && w == f.warm[0].Address() && id == 1 && live, nil
	})
	f.apply(testutil.WarmKeyBindTx(1, f.warm[0].Address(), 1), testutil.StartTx(1))
	old, err = f.sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	for range lag {
		_, err = f.oracle.(interface {
			AdvanceOne() (*blocks.Header, error)
		}).AdvanceOne()
		require.NoError(t, err)
	}
	rec, ok := f.sm.GetInference(1)
	require.True(t, ok)
	height, hash := state.StartStamp(&rec)
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1, ObservedHeight: height, ObservedBlockHash: hash,
		ExecutorSig: testutil.SignExecutorReceipt(t, f.warm[0], "warm-recovery", 1, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, testutil.ReceiptStamp{Height: height, Hash: hash}),
	}}}
	msg := &types.MsgFinishInference{InferenceId: 1, ExecutorSlot: 1, EscrowId: "warm-recovery", ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash, InputTokens: 80, OutputTokens: 40, ObservedHeight: height, ObservedBlockHash: hash}
	msg.ProposerSig = testutil.SignProposerTx(t, f.warm[0], msg)
	finish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
	vd, err := f.sm.PreviewLocalBestEffort(2, []*types.DevshardTx{old, confirm, finish})
	require.NoError(t, err)
	require.Len(t, vd.Applied, 3, "the regression must actually contain the fabricated work")
	diff := testutil.SignDiffWithRoot(t, f.user, "warm-recovery", 2, vd.Applied, vd.Root)
	require.True(t, f.sm.CommitValidated(vd))
	fresh, err := f.sm.BuildMainnetHeightTx(context.Background())
	require.NoError(t, err)
	next, err := f.sm.PreviewLocalBestEffort(3, []*types.DevshardTx{fresh})
	require.NoError(t, err)
	tail := testutil.SignDiffWithRoot(t, f.user, "warm-recovery", 3, next.Applied, next.Root)
	// Restore the receiving SM to the legitimate pre-attack prefix.
	before, err := state.NewStateMachine("warm-recovery", cfg, f.group, 1_000_000, f.user.Address(), signing.NewSecp256k1Verifier(), f.store, state.WithVersion("v7"), state.WithMainnetHeights(f.oracle.(state.MainnetHeightSource)), state.WithWarmKeyHeightChecker(func(c, w string, id uint64, h int64, at time.Time) (bool, error) {
		live := h < cfg.MainnetAnchorHeight+1
		if expiry {
			live = !at.After(expiryTime)
		}
		return c == f.cold[1].Address() && w == f.warm[0].Address() && id == 1 && live, nil
	}))
	require.NoError(t, err)
	records, err := f.store.GetDiffs("warm-recovery", 1, 1)
	require.NoError(t, err)
	require.Len(t, records, 1)
	_, err = before.ApplyLocalPersisted(1, records[0].Txs)
	require.NoError(t, err)
	f.sm = before
	return f, diff, tail
}

func TestBackdatedRetiredWarmWorkRejectedBeforePersistence(t *testing.T) {
	for _, lag := range []int{1, 5} {
		for _, path := range []string{"request", "gossip", "catch-up", "memory"} {
			t.Run(fmt.Sprintf("lag-%d/%s", lag, path), func(t *testing.T) {
				f, diff, tail := retiredWork(t, lag, false)
				before, err := f.sm.ComputeStateRoot()
				require.NoError(t, err)
				var h *Host
				if path == "memory" {
					h, err = NewHost(f.sm, f.cold[0], stub.NewInferenceEngine(), "warm-recovery", f.group, nil, WithChainOracle(f.oracle), WithGrace(100))
					require.NoError(t, err)
					t.Cleanup(h.Close)
				} else {
					h = f.host(f.cold[0], stub.NewInferenceEngine(), WithGrace(100))
				}
				switch path {
				case "gossip":
					sigs, err := h.ApplyRecoveredDiffs(context.Background(), []types.Diff{diff, tail})
					require.ErrorIs(t, err, types.ErrInvalidWarmKeyEntry)
					require.Empty(t, sigs)
				case "catch-up":
					h.ApplyCatchUpDiffs([]types.Diff{diff, tail})
				default:
					response, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff, tail}, Nonce: 3})
					require.ErrorIs(t, err, types.ErrInvalidWarmKeyEntry)
					require.Nil(t, response)
				}
				require.Equal(t, uint64(1), h.LatestNonce())
				after, err := f.sm.ComputeStateRoot()
				require.NoError(t, err)
				require.Equal(t, before, after)
				require.Zero(t, f.sm.SnapshotState().HostStats[1].Cost)
				records, err := f.store.GetDiffs("warm-recovery", 2, 3)
				require.NoError(t, err)
				require.Empty(t, records)
			})
		}
	}
}

func TestBackdatedExpiredWarmWorkRejectedWithinHeightLag(t *testing.T) {
	f, diff, _ := retiredWork(t, 2, true)
	h := f.host(f.cold[0], stub.NewInferenceEngine(), WithGrace(100))
	_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}, Nonce: 2})
	require.ErrorIs(t, err, types.ErrInvalidWarmKeyEntry)
	require.Equal(t, uint64(1), h.LatestNonce())
}

func TestWarmAdmissionUnavailableViewDoesNotMutateAndRetries(t *testing.T) {
	f, diff, _ := retiredWork(t, 1, false)
	var unavailable atomic.Bool
	unavailable.Store(true)
	anchor := f.sm.SnapshotState().Config.MainnetAnchorHeight
	f.sm.SetWarmKeyHeightChecker(func(c, w string, id uint64, h int64, at time.Time) (bool, error) {
		if unavailable.Load() && h > anchor {
			return false, fmt.Errorf("grant source behind admission height")
		}
		return true, nil
	})
	h := f.host(f.cold[0], stub.NewInferenceEngine(), WithGrace(100))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := h.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{diff}, Nonce: 2})
	require.ErrorIs(t, err, types.ErrHeaderPending)
	require.Equal(t, uint64(1), h.LatestNonce())
	records, err := f.store.GetDiffs("warm-recovery", 2, 2)
	require.NoError(t, err)
	require.Empty(t, records)
	unavailable.Store(false)
	response, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}, Nonce: 2})
	require.NoError(t, err)
	require.NotEmpty(t, response.StateSig)
	require.Equal(t, uint64(2), h.LatestNonce())
}

func TestHostAdmissionReplayPreservesAcceptedHistoryButRechecksLegacyRows(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(fmt.Sprintf("admitted-%t", accepted), func(t *testing.T) {
			f, diff, _ := retiredWork(t, 1, false)
			// A true row models prior admission while the grant was live. The false
			// case models the old implementation's unchecked on-disk work.
			require.NoError(t, f.store.AppendDiff("warm-recovery", types.DiffRecord{Diff: diff, StateHash: diff.PostStateRoot, HostAdmitted: accepted}))
			records, err := f.store.GetDiffs("warm-recovery", 2, 2)
			require.NoError(t, err)
			root, err := ReplayStoredDiff(context.Background(), f.sm, f.store, "warm-recovery", records[0])
			if accepted {
				require.NoError(t, err)
				require.Equal(t, diff.PostStateRoot, root)
				require.Equal(t, uint64(120), f.sm.SnapshotState().HostStats[1].Cost)
			} else {
				require.ErrorIs(t, err, types.ErrInvalidWarmKeyEntry)
				require.Equal(t, uint64(1), f.sm.LatestNonce())
				meta, err := f.store.GetSessionMeta("warm-recovery")
				require.NoError(t, err)
				require.Equal(t, uint64(1), meta.HostAdmissionNonce)
			}
		})
	}
}

func TestFreshTailCannotSignAnUncheckedStoredPrefix(t *testing.T) {
	f, diff, tail := retiredWork(t, 1, false)
	require.NoError(t, f.store.AppendDiff("warm-recovery", types.DiffRecord{Diff: diff, StateHash: diff.PostStateRoot}))
	_, err := f.sm.ApplyDiff(diff)
	require.NoError(t, err) // legacy in-memory recovery
	h := f.host(f.cold[0], stub.NewInferenceEngine(), WithGrace(100))
	response, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{tail}, Nonce: 3})
	require.ErrorIs(t, err, storage.ErrHostAdmissionGap)
	require.Nil(t, response)
	records, err := f.store.GetDiffs("warm-recovery", 3, 3)
	require.NoError(t, err)
	require.Empty(t, records)
	_, _, err = h.signState(t.Context(), 2, diff.PostStateRoot)
	require.ErrorIs(t, err, types.ErrHeaderPending)
}

type unavailableAdmissionTip struct{ state.MainnetHeightSource }

func (s unavailableAdmissionTip) Latest(context.Context) (*blocks.Header, error) {
	return nil, fmt.Errorf("mainnet tip unavailable")
}

func TestWarmAdmissionMissingTipLeavesWorkUnwritten(t *testing.T) {
	f, diff, _ := retiredWork(t, 1, false)
	cfg := f.sm.SnapshotState().Config
	sm, err := state.NewStateMachine("warm-recovery", cfg, f.group, 1_000_000, f.user.Address(), signing.NewSecp256k1Verifier(), f.store,
		state.WithVersion("v7"), state.WithMainnetHeights(unavailableAdmissionTip{f.oracle.(state.MainnetHeightSource)}),
		state.WithWarmKeyHeightChecker(func(string, string, uint64, int64, time.Time) (bool, error) { return true, nil }))
	require.NoError(t, err)
	records, err := f.store.GetDiffs("warm-recovery", 1, 1)
	require.NoError(t, err)
	_, err = sm.ApplyLocalPersisted(1, records[0].Txs)
	require.NoError(t, err)
	f.sm = sm
	h := f.host(f.cold[0], stub.NewInferenceEngine(), WithGrace(100))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = h.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{diff}, Nonce: 2})
	require.ErrorIs(t, err, types.ErrHeaderPending)
	require.Equal(t, uint64(1), h.LatestNonce())
	records, err = f.store.GetDiffs("warm-recovery", 2, 2)
	require.NoError(t, err)
	require.Empty(t, records)
}

func TestHostAdmissionSharedPrefixReplaysAfterRetirement(t *testing.T) {
	f, diff, tail := retiredWork(t, 1, false)
	// A sibling accepted nonce 2 while the key was live. This receiver is still
	// at nonce 1 when it receives the old diff and a fresh tail after revocation.
	require.NoError(t, f.store.AppendDiff("warm-recovery", types.DiffRecord{Diff: diff, StateHash: diff.PostStateRoot, HostAdmitted: true}))
	h := f.host(f.cold[0], stub.NewInferenceEngine(), WithGrace(100))
	response, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff, tail}, Nonce: 3})
	require.NoError(t, err)
	require.NotEmpty(t, response.StateSig)
	root, err := f.sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, tail.PostStateRoot, root)
	require.Equal(t, uint64(3), h.LatestNonce())
	meta, err := f.store.GetSessionMeta("warm-recovery")
	require.NoError(t, err)
	require.Equal(t, uint64(3), meta.HostAdmissionNonce)
}
