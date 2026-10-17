package host

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// laggingTipErrorExecutor is an honest executor that is behind the verifier's
// journal. It answers the tip read (SessionHead) with an answered, non-NotFound,
// non-unreachable error, so the verifier never learns the executor's applied
// nonce. Its ChallengeReceipt returns a verifying receipt only once it has been
// handed the catch-up diffs it is missing; challenged with the payload alone it
// has nothing to validate against and returns no receipt.
type laggingTipErrorExecutor struct {
	receipt      []byte
	confirm      *types.DevshardTx
	headCalls    int
	receiptCalls int
	diffsSeen    []int
}

func (p *laggingTipErrorExecutor) SessionHead(context.Context) (uint64, []byte, error) {
	p.headCalls++
	// errAnswered ("http 409") is classified by progressUnreachable as answered
	// (not unreachable) and is not sessionNotFound.
	return 0, nil, errAnswered
}

func (p *laggingTipErrorExecutor) GetMempool(context.Context) ([]*types.DevshardTx, error) {
	return nil, nil
}

func (p *laggingTipErrorExecutor) ChallengeCatchUp(context.Context, uint64, []types.DiffWire, string) error {
	return nil
}

func (p *laggingTipErrorExecutor) ChallengeReceipt(_ context.Context, _ uint64, _ *InferencePayload, diffs []types.Diff) ([]byte, []*types.DevshardTx, error) {
	p.receiptCalls++
	p.diffsSeen = append(p.diffsSeen, len(diffs))
	if len(diffs) == 0 {
		return nil, nil, nil
	}
	return p.receipt, []*types.DevshardTx{p.confirm}, nil
}

// TestRefusalAnsweredTipErrorCatchesUpLaggingExecutor pins the refused-timeout
// behaviour when SessionHead returns an answered error that is neither NotFound
// nor unreachable (for example an internal error from the executor's state-root
// computation, or FailedPrecondition/PermissionDenied while a session restarts
// or rotates). The verifier cannot read the executor's tip, so it must fall back
// to a cold catch-up from nonce 1 and hand the executor the diffs it is missing.
// A lagging but honest executor then produces a verifying receipt and the
// verifier votes to reject the timeout, so the executor keeps its reserved cost
// and is not charged a miss over the verifier's own inability to read the tip.
func TestRefusalAnsweredTipErrorCatchesUpLaggingExecutor(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	st := progressState(3) // the verifier holds diffs 1..3; the executor is behind
	g := newEvidenceGroup(t)
	confirm, sig := signedConfirm(t, g.signers[1], st.EscrowID, st, 1, 2000)
	peer := &laggingTipErrorExecutor{receipt: sig, confirm: confirm}
	journal := &progressJournal{diffs: progressDiffs(3, root)}

	accept, err := VerifyRefusedProgress(context.Background(), st, 1, st.Inferences[1], testPayload(), nil,
		peer, nil, g.sm, peer, journal, time.Second, 2*time.Second, deadlinePassedRefused(st, 1), progressUnreachable)
	require.NoError(t, err)

	// The executor only failed to answer the tip read. Caught up from nonce 1 with
	// the diffs it was missing, it proves progress, so the timeout is rejected and
	// the honest executor is not penalised.
	require.False(t, accept, "a lagging executor caught up from nonce 1 must not be penalised")
	require.Equal(t, 1, peer.headCalls)
	require.Equal(t, 1, peer.receiptCalls)
	require.Equal(t, []int{3}, peer.diffsSeen, "the executor was challenged with the full catch-up from nonce 1, not the payload alone")
}
