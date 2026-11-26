package engine

import (
	"context"
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"

	"devshard/types"
)

const clientFaultResponsePayload = `{"events":["data: {\"error\":{\"code\":400,\"message\":\"logit_bias key out of range\",\"type\":\"BadRequestError\"}}"]}`

func unbilledFinishTx(t *testing.T, nonce uint64) []byte {
	t.Helper()
	finishTx, err := proto.Marshal(&types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: nonce}}})
	if err != nil {
		t.Fatalf("proto.Marshal(): %v", err)
	}
	return finishTx
}

// Test flow:
//  1. Settle a `SettleByMissClaim` step over an unbilled client-fault Finish whose race siblings served nothing.
//  2. Settle the same step with one sibling the session holds as having served the prompt.
//  3. Assert the first posts neither a claim nor a vote and classifies as skipped for an unbilled client fault, not completed, and the second claims the miss naming that sibling.
func TestAClientFaultMissIsClaimedOnlyBesideAServedSibling(t *testing.T) {
	const nonce = uint64(7)
	finishTx := unbilledFinishTx(t, nonce)
	step := TimeoutStep{
		Nonce:    nonce,
		Kind:     SettleByMissClaim,
		Proof:    &MissProof{ResponsePayload: []byte(clientFaultResponsePayload), finishTx: finishTx},
		Siblings: []uint64{8, 9},
	}

	alone := &scriptedTimeoutHandler{}
	aloneVote, aloneErr := (&SessionTimeouts{handler: alone}).SettleTimeout(context.Background(), step)
	served := &scriptedTimeoutHandler{servedSiblings: map[uint64]bool{9: true}}
	if _, err := (&SessionTimeouts{handler: served}).SettleTimeout(context.Background(), step); err != nil {
		t.Fatalf("SettleTimeout() beside a served sibling = %v", err)
	}

	if alone.missCalls != 0 || alone.calls != 0 {
		t.Errorf("without a served sibling: %d claims and %d votes, want none", alone.missCalls, alone.calls)
	}
	if action, reason := TimeoutOutcome(aloneVote, aloneErr, false); action != TimeoutActionSkipped || reason != TimeoutReasonClientFault {
		t.Errorf("without a served sibling the outcome = %s/%s, want %s/%s", action, reason, TimeoutActionSkipped, TimeoutReasonClientFault)
	}
	if served.missCalls != 1 || served.claimedSibling != 9 {
		t.Errorf("beside a served sibling: %d claims naming %d, want 1 naming 9", served.missCalls, served.claimedSibling)
	}
}

// Test flow:
//  1. Plan the timeouts of a race whose first attempt claims a miss and whose other two attempts ran beside it.
//  2. Assert the claim step carries the other two attempts' nonces as its siblings, not its own.
func TestAMissClaimStepCarriesTheRacesOtherNonces(t *testing.T) {
	proof := &MissProof{ResponsePayload: []byte(clientFaultResponsePayload)}
	outcome := RaceOutcome{Attempts: []AttemptOutcome{
		{Nonce: 7, Terminal: TerminalErrorStream, MissProof: proof},
		{Nonce: 8, Terminal: TerminalWon},
		{Nonce: 9, Terminal: TerminalLost},
	}}

	for _, step := range outcome.TimeoutPlan() {
		if step.Nonce == 7 {
			if !reflect.DeepEqual(step.Siblings, []uint64{8, 9}) {
				t.Fatalf("claim step siblings = %v, want [8 9]", step.Siblings)
			}
			return
		}
	}
	t.Fatal("the plan has no step for the claimed nonce")
}
