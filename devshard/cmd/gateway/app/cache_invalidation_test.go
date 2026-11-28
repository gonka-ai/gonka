package app

import (
	"slices"
	"testing"

	"devshard/types"
)

type invalidatedReply struct {
	escrowID string
	nonce    uint64
}

type repliesSpy struct{ invalidated []invalidatedReply }

func (s *repliesSpy) InvalidateCachedReply(escrowID string, nonce uint64) {
	s.invalidated = append(s.invalidated, invalidatedReply{escrowID, nonce})
}

type diffsSpy struct{ composed int }

func (s *diffsSpy) DiffComposed(string, *types.Diff) { s.composed++ }

func validationTx(nonce uint64, valid bool) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{InferenceId: nonce, Valid: valid}}}
}

// Test flow:
//  1. Compose a diff carrying a valid vote on nonce 4, an invalid vote on nonce 5 and an invalid vote on nonce 6.
//  2. Hand it to the verdict-watching journal.
//  3. Assert the wrapped journal got the diff and only the two invalid votes revoked a cached reply.
func TestAnInvalidVoteInAComposedDiffRevokesTheCachedReply(t *testing.T) {
	spy := &repliesSpy{}
	diffs := &diffsSpy{}
	watching := verdictWatchingDiffs{diffs: diffs, replies: spy}

	watching.DiffComposed("escrow-1", &types.Diff{Txs: []*types.DevshardTx{validationTx(4, true), validationTx(5, false), validationTx(6, false)}})

	want := []invalidatedReply{{"escrow-1", 5}, {"escrow-1", 6}}
	if diffs.composed != 1 || !slices.Equal(spy.invalidated, want) {
		t.Fatalf("composed %d diffs, revoked %v, want 1 and %v", diffs.composed, spy.invalidated, want)
	}
}
