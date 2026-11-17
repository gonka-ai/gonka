package scheduler

import (
	"testing"
	"time"
)

// Test flow:
//  1. Spend the replay credit for "host-a" on "escrow-1".
//  2. Assert spending it again for the same pair fails.
//  3. Assert spending it for a different participant on the same escrow succeeds.
//  4. Assert spending it for the same participant on a different escrow succeeds.
func TestReplayCreditIsSpentOncePerParticipantAndEscrow(t *testing.T) {
	var credit replayCredit
	now := time.Unix(1_700_000_000, 0)

	if !credit.spend("escrow-1", "host-a", now) {
		t.Fatal("the first divergence must find the replay unspent")
	}
	if credit.spend("escrow-1", "host-a", now.Add(time.Second)) {
		t.Error("the second divergence must find it spent")
	}
	if !credit.spend("escrow-1", "host-b", now) {
		t.Error("another participant keeps its own replay")
	}
	if !credit.spend("escrow-2", "host-a", now) {
		t.Error("the same participant keeps a replay on another escrow")
	}
}

// Test flow:
//  1. Spend the replay credit at a rewind time, then for each table case restore it with a send timestamped before, at, or after the rewind.
//  2. Attempt to spend the credit again.
//  3. Assert the credit is available again only for the case where the send started after the rewind.
func TestOnlyASendStartedAfterTheRewindRestoresTheReplay(t *testing.T) {
	rewoundAt := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name    string
		sentAt  time.Time
		restore bool
	}{
		{"a send started after the rewind", rewoundAt.Add(time.Second), true},
		{"a send already in flight when it happened", rewoundAt.Add(-time.Second), false},
		{"a send started in the same instant", rewoundAt, false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			var credit replayCredit
			credit.spend("escrow-1", "host-a", rewoundAt)

			credit.restore("escrow-1", "host-a", testCase.sentAt)

			if got := credit.spend("escrow-1", "host-a", rewoundAt.Add(time.Hour)); got != testCase.restore {
				t.Errorf("replay available again = %v, want %v", got, testCase.restore)
			}
		})
	}
}

// Test flow:
//  1. Spend the replay credit for "host-a" on "escrow-1".
//  2. Forget the escrow.
//  3. Assert the credit can be spent again, since a retired escrow leaves no credit behind.
func TestForgettingAnEscrowDropsItsCredits(t *testing.T) {
	var credit replayCredit
	now := time.Unix(1_700_000_000, 0)
	credit.spend("escrow-1", "host-a", now)

	credit.forget("escrow-1")

	if !credit.spend("escrow-1", "host-a", now) {
		t.Error("a retired escrow must leave no credit behind")
	}
}
