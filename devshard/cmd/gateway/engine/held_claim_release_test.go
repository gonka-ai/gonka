package engine

import (
	"testing"
)

// Test flow:
//  1. Hold a suspicious host's finished claim beside one rival: one that missed its first-token or its receipt deadline without a token, one that missed with only a role chunk, one still inside its deadlines, one that missed but has streamed content, and one whose deadline was excused.
//  2. Settle the held claims.
//  3. Assert only the rival that missed with no content to show releases the claim: a silent rival past its deadline cannot be waited on for minutes, and a role chunk is not content.
func TestAHeldClaimIsReleasedOnceEveryRivalMissedItsDeadline(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		rival        *liveAttempt
		wantReleased bool
	}{
		{"a rival past its first-token deadline with nothing", &liveAttempt{nonce: 501, participant: "host-1", firstTokenDeadline: deadlineMissed, cancel: func() {}}, true},
		{"a rival past its receipt deadline", &liveAttempt{nonce: 501, participant: "host-1", receiptDeadline: deadlineMissed, cancel: func() {}}, true},
		{"a rival still inside its deadlines", &liveAttempt{nonce: 501, participant: "host-1", cancel: func() {}}, false},
		{"a rival past its deadline with only a role chunk", &liveAttempt{nonce: 501, participant: "host-1", firstTokenDeadline: deadlineMissed, firstToken: testEpoch, cancel: func() {}}, true},
		{"a rival past its deadline that has streamed content", &liveAttempt{nonce: 501, participant: "host-1", firstTokenDeadline: deadlineMissed, firstToken: testEpoch, firstContent: testEpoch, cancel: func() {}}, false},
		{"a rival whose deadline was excused", &liveAttempt{nonce: 501, participant: "host-1", firstTokenDeadline: deadlineExcused, cancel: func() {}}, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			suspicious := &liveAttempt{nonce: 500, participant: "host-0", suspicious: true, firstToken: testEpoch, cancel: func() {}}
			coordinator := pausedCoordinator(newRaceFixture(settledPolicy(), 2), 2, suspicious, testCase.rival)
			reply := make(chan streamVerdict, 1)
			coordinator.answer(crownRequest{Nonce: 500, Reply: reply})

			coordinator.settleClaims()

			released := coordinator.winner == suspicious
			if released != testCase.wantReleased {
				t.Fatalf("held claim released = %v, want %v", released, testCase.wantReleased)
			}
			if testCase.wantReleased && <-reply != streamWinner {
				t.Fatal("the released claim was not told it won")
			}
		})
	}
}
