package broker

import (
	"testing"
	"time"
)

// TestStopShutsDownNodeWorkers guards the fix for the Stop() goroutine leak:
// Stop must tear down the per-node workers, not just the broker-level loops, so
// a throwaway broker (selfcheck, tests) leaves nothing polling behind it.
func TestStopShutsDownNodeWorkers(t *testing.T) {
	b := NewTestBroker()

	// Register a live per-node worker directly. The RegisterNode command path
	// needs governance wiring we don't care about here; we only need a running
	// worker goroutine for Stop to reclaim.
	worker := NewNodeWorker("n1", &NodeWithState{Node: Node{Id: "n1"}}, b)
	b.nodeWorkGroup.AddWorker("n1", worker)
	if _, ok := b.nodeWorkGroup.GetWorker("n1"); !ok {
		t.Fatal("expected a node worker to be registered")
	}

	// Stop must return: if teardown deadlocked (e.g. a draining worker blocked on
	// a queue the command loop no longer drains), this would hang.
	done := make(chan struct{})
	go func() {
		b.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Broker.Stop did not return; node-worker teardown likely deadlocked")
	}

	// After Stop the worker must be gone, not just signalled.
	if _, ok := b.nodeWorkGroup.GetWorker("n1"); ok {
		t.Fatal("node worker still registered after Stop")
	}

	// Idempotent: a second Stop must neither panic nor block.
	b.Stop()
}
