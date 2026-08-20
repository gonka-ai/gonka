package localstate_test

import (
	"context"
	"testing"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/infrastructure/repositories/localstate"
	"trainshard/internal/utils/timex"
)

const ttl = time.Hour

var (
	node   = vo.NodeRef{Participant: "gonka1host", NodeID: "node-1"}
	now    = time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	answer = []run.NodeResult{{
		Node:  node,
		State: vo.ContainerRunning,
		Image: "sha256:abc",
		Fault: &shared.Fault{Code: "IMAGE_MISSING", Reason: "not pulled"},
	}}
)

func openLog(t *testing.T, dir string, clock *timex.Frozen) run.RequestLog {
	t.Helper()

	store, err := localstate.New(dir)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	return store.Requests(clock, ttl)
}

func TestARecordedAnswerSurvivesARestartOfTheDaemon(t *testing.T) {

	dir, clock := t.TempDir(), timex.NewFrozen(now)
	if err := openLog(t, dir, clock).Record(context.Background(), "req-1", answer); err != nil {
		t.Fatalf("record: %v", err)
	}

	replayed, found, err := openLog(t, dir, clock).Result(context.Background(), "req-1")

	if err != nil || !found {
		t.Fatalf("got found=%v err=%v, want the answer to outlive the process", found, err)
	}
	if len(replayed) != 1 || replayed[0].Node != node || replayed[0].State != vo.ContainerRunning {
		t.Fatalf("got %+v, want the recorded results back", replayed)
	}
	if replayed[0].Fault == nil || replayed[0].Fault.Code != "IMAGE_MISSING" {
		t.Fatalf("got %+v, want a replay to carry the code it carried the first time", replayed[0].Fault)
	}
}

func TestAnAnswerIsForgottenOnceItsTimeToLiveHasPassed(t *testing.T) {

	dir, clock := t.TempDir(), timex.NewFrozen(now)
	log := openLog(t, dir, clock)
	if err := log.Record(context.Background(), "req-1", answer); err != nil {
		t.Fatalf("record: %v", err)
	}
	clock.Advance(ttl + time.Minute)

	_, found, err := log.Result(context.Background(), "req-1")

	if err != nil || found {
		t.Fatalf("got found=%v err=%v, want a stale answer treated as never seen", found, err)
	}
}
