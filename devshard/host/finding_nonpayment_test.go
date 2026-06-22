package host

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

// newHostProdWiring builds a Host the way production (manager.hostOptions) does
// after the fix: checker == nil plus host.WithGrace from the escrow-frozen
// InferenceSealGraceNonces, so the executor withholds on a censored finish.
func newHostProdWiring(t *testing.T, hostIdx int, hosts []*signing.Secp256k1Signer, user *signing.Secp256k1Signer, balance uint64) *Host {
	t.Helper()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine(
		"escrow-1", config, group, balance, user.Address(), verifier,
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, balance),
	)
	require.NoError(t, err)
	engine := stub.NewInferenceEngine()
	// Mirror fixed production wiring: checker == nil, WithGrace installed.
	h, err := NewHost(sm, hosts[hostIdx], engine, "escrow-1", group, nil,
		WithGrace(uint64(config.InferenceSealGraceNonces)),
	)
	require.NoError(t, err)
	return h
}

// TestHost_Finding1_NonPayment_NoWithholdOnCensoredFinish is the regression
// guard for the host non-payment fix. The user (sequencer) censors the
// executor's MsgFinishInference -- the only tx that credits HostStats.Cost --
// leaving the host uncredited for a delivered response. With host.WithGrace
// wired in, the host MUST withhold its state signature once the finish goes
// stale, denying the user the settlement quorum. This failed before the fix.
func TestHost_Finding1_NonPayment_NoWithholdOnCensoredFinish(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)

	// Host index 1 is the executor for inference 1 (1 % len(group) == 1).
	const execHostIdx = 1
	h := newHostProdWiring(t, execHostIdx, hosts, user, 100000)

	// Nonce 1: user starts inference 1; executor runs it and produces a finish.
	diff1 := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := handleAndExecute(t, h, context.Background(), HostRequest{
		Diffs: []types.Diff{diff1}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, findMempoolFinish(resp.Mempool),
		"executor must produce MsgFinishInference for the work it just performed")

	// Nonces 2..50: user censors the finish and advances with empty diffs,
	// well past the grace window (floor 20).
	var lastResp *HostResponse
	const lastNonce = uint64(50)
	for nonce := uint64(2); nonce <= lastNonce; nonce++ {
		diff := testutil.SignDiff(t, user, "escrow-1", nonce, nil)
		lastResp, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
		require.NoError(t, err)
	}

	// The finish is still stuck in the executor's mempool, never applied.
	require.NotNil(t, findMempoolFinish(h.MempoolTxs()),
		"finish remains un-included after 49 nonces of censorship")

	// The inference never reached Finished and the executor slot is uncredited.
	st := h.sm.SnapshotState()
	rec := st.Inferences[1]
	require.NotNil(t, rec, "inference 1 must exist in state")
	require.NotEqual(t, types.StatusFinished, rec.Status,
		"inference never reached Finished because confirm/finish were censored")

	execSlot := h.group[1%uint64(len(h.group))].SlotID
	if hs := st.HostStats[execSlot]; hs != nil {
		require.Equal(t, uint64(0), hs.Cost,
			"executor uncredited (HostStats.Cost == 0) despite delivering the response")
	}

	// Regression guard: after the censored finish goes stale, the host MUST
	// withhold its state signature. False before the fix, true after.
	require.Nil(t, lastResp.StateSig,
		"REGRESSION: host signed nonce %d despite its MsgFinishInference being "+
			"censored; host.WithGrace must stay wired in manager.hostOptions.", lastNonce)
}
