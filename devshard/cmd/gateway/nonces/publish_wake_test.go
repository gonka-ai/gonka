package nonces

import (
	"context"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/registry"
)

const sweepWaitLimit = 2 * time.Second

type countingEscrowSource struct {
	swept chan struct{}
}

func (s countingEscrowSource) Snapshot() []registry.EscrowState {
	s.swept <- struct{}{}
	return nil
}

func (countingEscrowSource) RoutableSession(string) (registry.EscrowSession, bool) {
	return nil, false
}

func awaitSweep(t *testing.T, swept <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-swept:
	case <-time.After(sweepWaitLimit):
		t.Fatalf("no sweep %s within %s", what, sweepWaitLimit)
	}
}

// Test flow:
//  1. Run the sweep loop, whose own interval is far longer than this test waits, and let its first pass run.
//  2. Publish an escrow.
//  3. Assert a second pass runs at once: the ledger opens a new escrow before the facts of its first requests arrive, rather than dropping them until the next interval.
func TestPublishingAnEscrowWakesTheLedgerSweep(t *testing.T) {
	ledger := newLedgerForTest(t)
	source := countingEscrowSource{swept: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	var running sync.WaitGroup
	running.Go(func() { ledger.sweepUntil(ctx, source, nil) })
	t.Cleanup(func() {
		cancel()
		running.Wait()
	})
	awaitSweep(t, source.swept, "at start")

	ledger.EscrowPublished("escrow-2", "model-a")

	awaitSweep(t, source.swept, "after the publication")
}
