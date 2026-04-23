//go:build stress

package user

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"subnet/host"
	"subnet/internal/testutil"
	"subnet/signing"
	"subnet/state"
	"subnet/stub"
	"subnet/types"
)

// KillableClient wraps a HostClient. Once Kill() is called, all Send calls
// return an error, simulating a dead host.
type KillableClient struct {
	inner  HostClient
	killed atomic.Bool
}

func (c *KillableClient) Send(ctx context.Context, req host.HostRequest) (*host.HostResponse, error) {
	if c.killed.Load() {
		return nil, fmt.Errorf("host killed")
	}
	return c.inner.Send(ctx, req)
}

func (c *KillableClient) Kill() { c.killed.Store(true) }

const (
	faultNumHosts   = 16
	faultBalance    = 10_000_000
	faultModel      = "llama-3.1-70b"
	faultInputLen   = 200
	faultMaxTokens  = 100
	faultCostPerInf = 120 // stub: (80+40)*1
	faultRounds     = 3   // 3 full rounds = 48 total inferences
)

func TestFault(t *testing.T) {
	t.Run("10pct", func(t *testing.T) { runFault(t, 10) })
	t.Run("20pct", func(t *testing.T) { runFault(t, 20) })
	t.Run("30pct", func(t *testing.T) { runFault(t, 30) })
	t.Run("40pct", func(t *testing.T) { runFault(t, 40) })
}

func runFault(t *testing.T, failPct int) {
	totalInf := faultNumHosts * faultRounds
	halfInf := totalInf / 2
	grace := uint64(totalInf + 100)

	// --- Setup ---
	hostSigners := make([]*signing.Secp256k1Signer, faultNumHosts)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    uint32(faultNumHosts) / 2,
		ValidationRate:   5000,
	}
	verifier := signing.NewSecp256k1Verifier()

	killables := make([]*KillableClient, faultNumHosts)
	clients := make([]HostClient, faultNumHosts)
	for i := range hostSigners {
		sm := state.NewStateMachine("escrow-fault", config, group, faultBalance, userKey.Address(), verifier)
		engine := stub.NewInferenceEngine()
		h, err := host.NewHost(sm, hostSigners[i], engine, "escrow-fault", group, nil, host.WithGrace(grace))
		require.NoError(t, err)
		kc := &KillableClient{inner: &ConcurrentClient{inner: &InProcessClient{Host: h}}}
		killables[i] = kc
		clients[i] = kc
	}

	userSM := state.NewStateMachine("escrow-fault", config, group, faultBalance, userKey.Address(), verifier)
	session, err := NewSession(userSM, userKey, "escrow-fault", group, clients, verifier)
	require.NoError(t, err)

	ctx := context.Background()
	params := InferenceParams{
		Model:       faultModel,
		Prompt:      stressPrompt,
		InputLength: faultInputLen,
		MaxTokens:   faultMaxTokens,
		StartedAt:   1000,
	}

	// --- Phase 1: healthy inferences ---
	for i := 0; i < halfInf; i++ {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err, "healthy inference %d failed", i+1)
	}
	healthyCount := halfInf

	// --- Kill hosts ---
	numDead := int(math.Ceil(float64(faultNumHosts) * float64(failPct) / 100.0))
	deadSlots := make(map[uint32]bool, numDead)
	// Kill hosts with highest slot IDs (deterministic).
	for i := faultNumHosts - numDead; i < faultNumHosts; i++ {
		killables[i].Kill()
		deadSlots[uint32(i)] = true
	}

	// --- Compute expected failures ---
	// Each inference at nonce n routes to host n % numHosts.
	// Degraded inferences have nonces from halfInf+1 to totalInf.
	expectedFailures := 0
	for n := halfInf + 1; n <= totalInf; n++ {
		hostIdx := n % faultNumHosts
		if deadSlots[uint32(hostIdx)] {
			expectedFailures++
		}
	}

	// --- Phase 2: degraded inferences ---
	var degradedSuccess, degradedFail int
	for i := 0; i < halfInf; i++ {
		_, err := session.SendInference(ctx, params)
		if err != nil {
			degradedFail++
		} else {
			degradedSuccess++
		}
	}

	require.Equal(t, expectedFailures, degradedFail,
		"expected %d failures from dead hosts, got %d", expectedFailures, degradedFail)

	// --- Nonce check ---
	// Nonce advances for every attempt (PrepareInference applies locally before send).
	// At this point, nonce == totalInf since Finalize hasn't run yet.
	require.Equal(t, uint64(totalInf), session.Nonce(),
		"nonce should advance for all %d attempts", totalInf)

	// --- Pre-finalize state checks ---
	stPre := session.StateMachine().SnapshotState()

	// All inferences are applied locally (PrepareInference runs before Send),
	// so every attempt appears in state regardless of send outcome.
	require.Len(t, stPre.Inferences, totalInf,
		"all %d inferences should exist in state", totalInf)

	// Count inferences by status for reporting.
	statusCountsPre := make(map[types.InferenceStatus]int)
	for _, rec := range stPre.Inferences {
		statusCountsPre[rec.Status]++
	}

	// Failed sends leave inferences without host response, so their confirm
	// and finish messages never arrive. Due to pipelining, the last
	// successful inference before a dead-host failure may also remain
	// unfinished. Verify that at least degradedFail inferences are pending.
	pendingCount := statusCountsPre[types.StatusPending]
	require.GreaterOrEqual(t, pendingCount, degradedFail,
		"at least %d inferences should be pending (got %d)", degradedFail, pendingCount)

	// Finished inferences should not exceed total successes.
	finishedCount := statusCountsPre[types.StatusFinished]
	require.LessOrEqual(t, finishedCount, healthyCount+degradedSuccess,
		"finished count %d should not exceed successful sends %d",
		finishedCount, healthyCount+degradedSuccess)

	// Balance check: each MsgStartInference reserves tokenPrice*(inputLength+maxTokens).
	// MsgFinishInference refunds (reserved - actual). Unfinished inferences keep the
	// full reserved cost locked.
	reservedCostPerInf := uint64(faultInputLen + faultMaxTokens)
	refundPerFinished := reservedCostPerInf - faultCostPerInf
	totalDeducted := uint64(totalInf)*reservedCostPerInf - uint64(finishedCount)*refundPerFinished
	expectedBalance := uint64(faultBalance) - totalDeducted
	require.Equal(t, expectedBalance, stPre.Balance,
		"balance: got %d expected %d", stPre.Balance, expectedBalance)

	// --- Phase 3: queue timeout txs for dead-host pending inferences ---

	// Build timeout verifiers from alive hosts.
	verifiers := map[int]TimeoutVerifier{}
	for i, slot := range group {
		if deadSlots[slot.SlotID] {
			continue
		}
		verifiers[i] = &mockTimeoutVerifier{
			accept:   true,
			signer:   hostSigners[i],
			group:    group,
			slotIdx:  i,
			escrowID: "escrow-fault",
		}
	}

	// Collect pending inferences routed to dead hosts (sorted for determinism).
	var pendingDeadIDs []uint64
	for id, rec := range stPre.Inferences {
		if rec.Status == types.StatusPending && deadSlots[rec.ExecutorSlot] {
			pendingDeadIDs = append(pendingDeadIDs, id)
		}
	}
	slices.Sort(pendingDeadIDs)

	payload := &host.InferencePayload{
		Prompt:      stressPrompt,
		Model:       faultModel,
		InputLength: faultInputLen,
		MaxTokens:   faultMaxTokens,
		StartedAt:   1000,
	}
	for _, infID := range pendingDeadIDs {
		votes, err := session.CollectTimeoutVotes(ctx, infID,
			types.TimeoutReason_TIMEOUT_REASON_REFUSED, payload, verifiers, nil)
		require.NoError(t, err, "collect timeout votes for inference %d", infID)
		session.addPendingTx(&types.SubnetTx{Tx: &types.SubnetTx_TimeoutInference{
			TimeoutInference: &types.MsgTimeoutInference{
				InferenceId: infID,
				Reason:      types.TimeoutReason_TIMEOUT_REASON_REFUSED,
				Votes:       votes,
			},
		}})
	}

	// --- Phase 4: Finalize (tolerates dead hosts if quorum is met) ---
	preFinNonce := session.Nonce()
	err = session.Finalize(ctx)

	aliveHosts := faultNumHosts - numDead
	threshold := 2*faultNumHosts/3 + 1
	expectSuccess := aliveHosts >= threshold
	if expectSuccess {
		require.NoError(t, err, "finalize should succeed with %d/%d alive (threshold %d)",
			aliveHosts, faultNumHosts, threshold)
	} else {
		require.Error(t, err, "finalize should fail with %d/%d alive (threshold %d)",
			aliveHosts, faultNumHosts, threshold)
		require.Contains(t, err.Error(), "insufficient signatures")
	}
	postFinNonce := session.Nonce()

	// --- Post-finalize state snapshot ---
	st := session.StateMachine().SnapshotState()

	// --- Signature check ---
	// Dead hosts may have signed during the healthy phase (before kill).
	// After the kill nonce, no dead host should have new signatures.
	killNonce := uint64(halfInf)
	sigs := session.Signatures()
	for nonce, slotSigs := range sigs {
		if nonce <= killNonce {
			continue
		}
		for slotID := range slotSigs {
			require.False(t, deadSlots[slotID],
				"dead slot %d should not have signatures after kill (nonce %d)", slotID, nonce)
		}
	}

	// Count alive hosts that signed at any nonce.
	signedAlive := make(map[uint32]bool)
	for _, slotSigs := range sigs {
		for slotID := range slotSigs {
			if !deadSlots[slotID] {
				signedAlive[slotID] = true
			}
		}
	}

	// --- Post-finalize status counts ---
	statusCounts := make(map[types.InferenceStatus]int)
	for _, rec := range st.Inferences {
		statusCounts[rec.Status]++
	}

	// --- Timeout assertions ---
	// All pending dead-host inferences should now be timed out.
	timedOutCount := statusCounts[types.StatusTimedOut]
	require.Equal(t, len(pendingDeadIDs), timedOutCount,
		"timed out inferences: expected %d, got %d", len(pendingDeadIDs), timedOutCount)

	// Dead executor slots with pending inferences should have missed > 0.
	deadSlotsWithMissed := make(map[uint32]bool)
	for _, infID := range pendingDeadIDs {
		rec := st.Inferences[infID]
		deadSlotsWithMissed[rec.ExecutorSlot] = true
	}
	totalMissed := 0
	for slot := range deadSlotsWithMissed {
		missed := st.HostStats[slot].Missed
		require.Greater(t, missed, uint32(0),
			"dead slot %d should have missed > 0", slot)
		totalMissed += int(missed)
	}
	require.Equal(t, len(pendingDeadIDs), totalMissed,
		"total missed across dead slots should equal %d, got %d", len(pendingDeadIDs), totalMissed)

	// Balance: timed-out inferences refund reserved cost. Finalize also
	// applies seed reveals which settle finished inferences, so the exact
	// post-finalize balance is hard to predict. Verify balance increased
	// relative to pre-finalize by at least the timeout refund amount.
	timedOutRefund := uint64(len(pendingDeadIDs)) * reservedCostPerInf
	require.GreaterOrEqual(t, st.Balance, expectedBalance+timedOutRefund,
		"balance should increase by at least timeout refund: pre=%d post=%d refund=%d",
		expectedBalance, st.Balance, timedOutRefund)

	// At least some alive hosts should have RequiredValidations > 0
	// (from partial seed reveals during Finalize Phase A).
	aliveWithReqVal := 0
	for slot := uint32(0); slot < faultNumHosts; slot++ {
		if !deadSlots[slot] && st.HostStats[slot].RequiredValidations > 0 {
			aliveWithReqVal++
		}
	}
	// Finalize processes at least one alive host before hitting a dead one,
	// so we expect at least one alive host with req_val > 0.
	require.Greater(t, aliveWithReqVal, 0,
		"at least one alive host should have RequiredValidations > 0")

	// Dead hosts that didn't reveal seeds should be penalized:
	// RequiredValidations > 0 and CompletedValidations == 0.
	for slot := uint32(0); slot < faultNumHosts; slot++ {
		if !deadSlots[slot] {
			continue
		}
		hs := st.HostStats[slot]
		if st.RevealedSeeds[slot] != 0 {
			continue // revealed before dying (shouldn't happen, but guard)
		}
		require.Greater(t, hs.RequiredValidations, uint32(0),
			"dead slot %d should have RequiredValidations > 0 from penalty", slot)
		require.Equal(t, uint32(0), hs.CompletedValidations,
			"dead slot %d should have CompletedValidations == 0", slot)
	}

	// --- Report ---
	t.Logf("")
	t.Logf("--- fault test report (%d%% failure) ---", failPct)
	t.Logf("config: hosts=%d dead=%d alive=%d rounds=%d total_inferences=%d",
		faultNumHosts, numDead, faultNumHosts-numDead, faultRounds, totalInf)
	t.Logf("")
	t.Logf("inferences:")
	t.Logf("  healthy=%d/%d degraded=%d/%d", healthyCount, halfInf, degradedSuccess, halfInf)
	t.Logf("  failures: expected=%d actual=%d", expectedFailures, degradedFail)
	t.Logf("  pre_finalize:  pending=%d started=%d finished=%d",
		statusCountsPre[types.StatusPending], statusCountsPre[types.StatusStarted], statusCountsPre[types.StatusFinished])
	t.Logf("  post_finalize: pending=%d started=%d finished=%d timed_out=%d",
		statusCounts[types.StatusPending], statusCounts[types.StatusStarted],
		statusCounts[types.StatusFinished], timedOutCount)
	t.Logf("")
	t.Logf("timeouts:")
	t.Logf("  dead-host pending inferences: %d", len(pendingDeadIDs))
	t.Logf("  timed out: %d", timedOutCount)
	t.Logf("  total missed: %d", totalMissed)
	t.Logf("")
	t.Logf("state:")
	t.Logf("  nonce: pre_finalize=%d post_finalize=%d (inferences=%d)",
		preFinNonce, postFinNonce, totalInf)
	t.Logf("  balance: pre=%d post=%d (timeout refund=%d)",
		expectedBalance, st.Balance, timedOutRefund)
	if expectSuccess {
		t.Logf("  finalize: succeeded (%d/%d alive >= threshold %d)", aliveHosts, faultNumHosts, threshold)
	} else {
		t.Logf("  finalize: failed as expected (%d/%d alive < threshold %d)", aliveHosts, faultNumHosts, threshold)
	}
	t.Logf("  alive hosts with req_val>0: %d", aliveWithReqVal)
	t.Logf("  dead hosts penalized (req_val>0, comp_val=0): %d", numDead)
	t.Logf("")
	t.Logf("signatures: %d/%d alive hosts signed", len(signedAlive), faultNumHosts-numDead)
	t.Logf("")
	t.Logf("host_stats:")
	for slot := uint32(0); slot < faultNumHosts; slot++ {
		hs := st.HostStats[slot]
		status := "alive"
		if deadSlots[slot] {
			status = "DEAD"
		}
		t.Logf("  slot %2d [%s]: cost=%d missed=%d invalid=%d req_val=%d comp_val=%d",
			slot, status, hs.Cost, hs.Missed, hs.Invalid, hs.RequiredValidations, hs.CompletedValidations)
	}
}
