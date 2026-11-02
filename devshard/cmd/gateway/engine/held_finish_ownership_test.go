package engine

import (
	"sync/atomic"
	"testing"

	"devshard/gatewayproto"
)

const attemptsOutlivingTheirRace = 200

// Test flow:
//  1. Close the race's report channel, as a race that already reported does, with room left in its event buffer.
//  2. Finish many attempts, each carrying a held error-miss Finish, into that buffer nobody reads any more.
//  3. Assert every held Finish was released: a send the coordinator never reads must not pin the Finish.
func TestAnAttemptFinishingIntoAReportedRacesBufferReleasesItsHeldFinish(t *testing.T) {
	reported := make(chan struct{})
	close(reported)
	spec := AttemptSpec{Events: make(chan AttemptEvent, attemptsOutlivingTheirRace), Reported: reported}
	var released atomic.Int64

	for range attemptsOutlivingTheirRace {
		outcome := &AttemptOutcome{MissProof: &MissProof{finish: gatewayproto.NewFinishHold(func() { released.Add(1) })}}
		spec.finish(AttemptEvent{Kind: AttemptDone, Outcome: outcome})
	}

	if got := released.Load(); got != attemptsOutlivingTheirRace {
		t.Fatalf("held Finishes released = %d, want %d", got, attemptsOutlivingTheirRace)
	}
}

// Test flow:
//  1. Let a coordinator receive an attempt's terminal event and take its held Finish, then report the race.
//  2. Finish the attempt through that hand-off.
//  3. Assert the attempt did not release the Finish: the race owns it now and releases it once its claim is posted.
func TestAFinishTheRaceTookIsNotReleasedByTheAttempt(t *testing.T) {
	events := make(chan AttemptEvent)
	reported := make(chan struct{})
	spec := AttemptSpec{Events: events, Reported: reported}
	var released atomic.Int64
	outcome := &AttemptOutcome{MissProof: &MissProof{finish: gatewayproto.NewFinishHold(func() { released.Add(1) })}}

	taken := make(chan struct{})
	go func() {
		event := <-events
		event.Outcome.MissProof.takeHeldFinish()
		close(reported)
		close(taken)
	}()
	spec.finish(AttemptEvent{Kind: AttemptDone, Outcome: outcome})
	<-taken

	if got := released.Load(); got != 0 {
		t.Fatalf("held Finish released %d times by the attempt, want 0: the race took it", got)
	}
}

func TestCopiedMissProofSharesItsFinishOwner(t *testing.T) {
	var released atomic.Int32
	proof := MissProof{finish: gatewayproto.NewFinishHold(func() { released.Add(1) })}
	copied := proof
	copied.takeHeldFinish()
	proof.releaseUntakenFinish()
	if released.Load() != 0 {
		t.Fatal("original released ownership transferred through copy")
	}
	proof.releaseHeldFinish()
	copied.releaseHeldFinish()
	if released.Load() != 1 {
		t.Fatal("copies released a Finish twice")
	}
}
