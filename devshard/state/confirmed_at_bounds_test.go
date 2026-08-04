package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

const oneYearSeconds = int64(365 * 24 * 3600)

func newBoundsTestSM(t *testing.T, escrowID string, hosts []*signing.Secp256k1Signer) (*StateMachine, []types.SlotAssignment) {
	t.Helper()
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	cfg := types.DefaultSessionConfig(len(hosts))
	cfg.CreateDevshardFee = 0
	cfg.FeePerNonce = 0
	sm, err := NewStateMachine(escrowID, cfg, group, 1_000_000, user.Address(),
		signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, escrowID, user.Address(), cfg, group, 1_000_000))
	require.NoError(t, err)
	return sm, group
}

func boundsTestHosts(t *testing.T, n int) []*signing.Secp256k1Signer {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, n)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	return hosts
}

func confirmInference(
	t *testing.T,
	sm *StateMachine,
	escrowID string,
	hosts []*signing.Secp256k1Signer,
	group []types.SlotAssignment,
	id, nonce uint64,
	startedAt, confirmedAt int64,
) error {
	t.Helper()
	execIdx := int(id % uint64(len(group))) // executor slot is group[id % len(group)]
	sig := testutil.SignExecutorReceipt(t, hosts[execIdx], escrowID, id,
		[]byte("prompt"), "llama", 10, 5, startedAt, confirmedAt)
	_, err := sm.ApplyLocal(nonce, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: id, ExecutorSig: sig, ConfirmedAt: confirmedAt,
	})})
	return err
}

func driveToFinished(
	t *testing.T,
	sm *StateMachine,
	escrowID string,
	hosts []*signing.Secp256k1Signer,
	group []types.SlotAssignment,
	id, startNonce uint64,
	startedAt, confirmedAt int64,
) {
	t.Helper()
	startInference(t, sm, id, startNonce, startedAt)
	require.NoError(t, confirmInference(t, sm, escrowID, hosts, group, id, startNonce+1, startedAt, confirmedAt))
	finishInference(t, sm, escrowID, hosts, group, id, startNonce+2)
}

func startInference(t *testing.T, sm *StateMachine, id, nonce uint64, startedAt int64) {
	t.Helper()
	_, err := sm.ApplyLocal(nonce, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: id, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 10, MaxTokens: 5, StartedAt: startedAt,
	})})
	require.NoError(t, err)
}

func finishInference(
	t *testing.T,
	sm *StateMachine,
	escrowID string,
	hosts []*signing.Secp256k1Signer,
	group []types.SlotAssignment,
	id, nonce uint64,
) {
	t.Helper()
	execIdx := int(id % uint64(len(group)))
	finish := &types.MsgFinishInference{
		InferenceId:  id,
		ResponseHash: []byte("response"),
		InputTokens:  2,
		OutputTokens: 3,
		ExecutorSlot: group[execIdx].SlotID,
		EscrowId:     escrowID,
	}
	finish.ProposerSig = testutil.SignProposerTx(t, hosts[execIdx], finish)
	_, err := sm.ApplyLocal(nonce, []*types.DevshardTx{txFinish(finish)})
	require.NoError(t, err)
}

func idleTo(t *testing.T, sm *StateMachine, from, to uint64) {
	t.Helper()
	for n := from; n <= to; n++ {
		_, err := sm.ApplyLocal(n, nil)
		require.NoError(t, err)
	}
}

func TestConfirmStart_ConfirmedAtBounds(t *testing.T) {
	const (
		escrowID  = "escrow-bounds"
		startedAt = int64(1_700_000_000)
	)

	cases := []struct {
		name        string
		confirmedAt int64
		wantErr     bool
	}{
		{"equal_to_started_at", startedAt, false},
		{"max_delay_exact", startedAt + types.MaxConfirmationDelaySeconds, false},
		{"max_delay_plus_one", startedAt + types.MaxConfirmationDelaySeconds + 1, true},
		{"max_skew_exact", startedAt - types.MaxConfirmationSkewSeconds, false},
		{"max_skew_plus_one", startedAt - types.MaxConfirmationSkewSeconds - 1, true},
		{"one_year_ahead", startedAt + oneYearSeconds, true},
		{"one_hour_behind", startedAt - 3600, true},
		{"zero", 0, true},
		{"negative", -1, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hosts := boundsTestHosts(t, 5)
			sm, group := newBoundsTestSM(t, escrowID, hosts)

			const id = uint64(1)
			startInference(t, sm, id, id, startedAt)
			err := confirmInference(t, sm, escrowID, hosts, group, id, id+1, startedAt, tc.confirmedAt)

			if tc.wantErr {
				require.ErrorIs(t, err, types.ErrInvalidConfirmedAt)
				rec, ok := sm.SnapshotState().Inferences[id]
				require.True(t, ok)
				require.Equal(t, types.StatusPending, rec.Status,
					"rejected confirm must not mutate the record")
				require.Zero(t, rec.ConfirmedAt)
				return
			}
			require.NoError(t, err)
			rec, ok := sm.SnapshotState().Inferences[id]
			require.True(t, ok)
			require.Equal(t, types.StatusStarted, rec.Status)
			require.Equal(t, tc.confirmedAt, rec.ConfirmedAt)
		})
	}
}

func TestConfirmStart_FutureConfirmedAtCannotPoisonSealClock(t *testing.T) {
	const (
		escrowID    = "escrow-clockpoison"
		victimID    = uint64(1)
		attackerID  = uint64(100)
		sealNonce   = uint64(150) // DefaultAutoSealEveryNNonces
		honestStart = int64(1_700_000_000)
	)

	hosts := boundsTestHosts(t, 5)
	sm, group := newBoundsTestSM(t, escrowID, hosts)

	st := sm.SnapshotState()
	require.Equal(t, uint32(20), st.Config.InferenceSealGraceNonces)
	require.Equal(t, uint32(3600), st.Config.InferenceSealGraceSeconds)

	driveToFinished(t, sm, escrowID, hosts, group, victimID, victimID, honestStart, honestStart+2)
	idleTo(t, sm, victimID+3, attackerID-1)

	startInference(t, sm, attackerID, attackerID, honestStart+600)
	err := confirmInference(t, sm, escrowID, hosts, group, attackerID, attackerID+1,
		honestStart+600, honestStart+600+oneYearSeconds)
	require.ErrorIs(t, err, types.ErrInvalidConfirmedAt,
		"a validly signed but out-of-bounds receipt must be rejected")

	clock := sm.AutoSealStateClock()
	require.True(t, clock.Known)
	require.Equal(t, honestStart+2, clock.Clock, "state clock must not be attacker-controlled")

	idleTo(t, sm, attackerID+1, sealNonce)

	require.Greater(t, sealNonce, victimID+uint64(st.Config.InferenceSealGraceNonces))
	rec, live := sm.SnapshotState().Inferences[victimID]
	require.True(t, live, "victim must survive the sweep: its genuine grace has not elapsed")
	require.Equal(t, types.StatusFinished, rec.Status)

	val := &types.MsgValidation{
		InferenceId:   victimID,
		ValidatorSlot: group[2].SlotID,
		Valid:         false,
		EscrowId:      escrowID,
	}
	val.ProposerSig = testutil.SignProposerTx(t, hosts[2], val)
	_, err = sm.ApplyLocal(sealNonce+1, []*types.DevshardTx{
		{Tx: &types.DevshardTx_Validation{Validation: val}},
	})
	require.NoError(t, err, "validation must remain possible for a live finished inference")
	require.Equal(t, types.StatusChallenged, sm.SnapshotState().Inferences[victimID].Status)
}

func TestConfirmStart_BackdatedConfirmedAtCannotSelfSeal(t *testing.T) {
	const (
		escrowID    = "escrow-backdate"
		badID       = uint64(10)  // attacker's own inference
		honestID    = uint64(100) // honest traffic that defines the clock
		sealNonce   = uint64(150)
		honestStart = int64(1_700_000_000)
	)

	hosts := boundsTestHosts(t, 5)
	sm, group := newBoundsTestSM(t, escrowID, hosts)

	grace := int64(sm.SnapshotState().Config.InferenceSealGraceSeconds)

	idleTo(t, sm, 1, badID-1)
	startInference(t, sm, badID, badID, honestStart)
	err := confirmInference(t, sm, escrowID, hosts, group, badID, badID+1, honestStart, honestStart-grace)
	require.ErrorIs(t, err, types.ErrInvalidConfirmedAt,
		"backdating past the skew bound must be rejected")

	require.NoError(t, confirmInference(t, sm, escrowID, hosts, group, badID, badID+1, honestStart, honestStart+1))
	finishInference(t, sm, escrowID, hosts, group, badID, badID+2)

	idleTo(t, sm, badID+3, honestID-1)
	driveToFinished(t, sm, escrowID, hosts, group, honestID, honestID, honestStart+600, honestStart+601)
	idleTo(t, sm, honestID+3, sealNonce)

	rec, live := sm.SnapshotState().Inferences[badID]
	require.True(t, live, "record must not self-seal ahead of its validation grace")
	require.Equal(t, types.StatusFinished, rec.Status)
	require.LessOrEqual(t, sm.AutoSealStateClock().Clock-rec.ConfirmedAt, grace)
}

func TestValidateConfirmedAt_BoundsStayBelowSealGrace(t *testing.T) {
	grace := int64(types.DefaultInferenceSealGraceSeconds)
	require.Less(t, types.MaxConfirmationDelaySeconds, grace,
		"a receipt could otherwise advance the seal clock past a full grace period")
	require.Less(t, types.MaxConfirmationSkewSeconds, grace,
		"a backdated receipt could otherwise expire its own grace on arrival")
}

func TestConfirmStart_RejectionKeepsUserAndHostInAgreement(t *testing.T) {
	const (
		escrowID    = "escrow-parity"
		id          = uint64(1)
		honestStart = int64(1_700_000_000)
	)

	hosts := boundsTestHosts(t, 5)
	group := testutil.MakeGroup(hosts)
	userSigner := testutil.MustGenerateKey(t)
	cfg := types.DefaultSessionConfig(len(hosts))
	cfg.CreateDevshardFee = 0
	cfg.FeePerNonce = 0

	newSM := func() *StateMachine {
		sm, err := NewStateMachine(escrowID, cfg, group, 1_000_000, userSigner.Address(),
			signing.NewSecp256k1Verifier(),
			testutil.MustMemoryStore(t, escrowID, userSigner.Address(), cfg, group, 1_000_000))
		require.NoError(t, err)
		return sm
	}
	userSM, hostSM := newSM(), newSM()

	startTx := txStart(&types.MsgStartInference{
		InferenceId: id, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 10, MaxTokens: 5, StartedAt: honestStart,
	})
	startDiff := testutil.SignDiff(t, userSigner, escrowID, 1, []*types.DevshardTx{startTx})
	userRoot, err := userSM.ApplyLocal(1, []*types.DevshardTx{startTx})
	require.NoError(t, err)
	hostRoot, err := hostSM.ApplyDiff(startDiff)
	require.NoError(t, err)
	require.Equal(t, userRoot, hostRoot)

	execIdx := int(id % uint64(len(group)))
	poisoned := honestStart + oneYearSeconds
	sig := testutil.SignExecutorReceipt(t, hosts[execIdx], escrowID, id,
		[]byte("prompt"), "llama", 10, 5, honestStart, poisoned)
	badConfirm := txConfirm(&types.MsgConfirmStart{
		InferenceId: id, ExecutorSig: sig, ConfirmedAt: poisoned,
	})

	userRoot, applied, err := userSM.ApplyLocalBestEffort(2, []*types.DevshardTx{badConfirm})
	require.NoError(t, err, "best-effort compose must not fail the whole diff")
	require.Empty(t, applied, "poisoned confirm must be dropped from the composed diff")

	emptyDiff := testutil.SignDiffWithRoot(t, userSigner, escrowID, 2, applied, userRoot)
	hostRoot, err = hostSM.ApplyDiff(emptyDiff)
	require.NoError(t, err, "host must accept the diff the user actually composed")
	require.Equal(t, userRoot, hostRoot, "user and host must agree after rejection")

	forcedDiff := testutil.SignDiff(t, userSigner, escrowID, 3, []*types.DevshardTx{badConfirm})
	_, err = hostSM.ApplyDiff(forcedDiff)
	require.ErrorIs(t, err, types.ErrInvalidConfirmedAt)
	require.Equal(t, uint64(2), hostSM.LatestNonce(), "rejected diff must not advance the host")
}
