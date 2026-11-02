package gatewayproto

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestGateCancellationDoesNotReleaseItsOwner(t *testing.T) {
	gate := NewGate()
	gate.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gate.LockContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want context cancellation", err)
	}
	if gate.TryLock() {
		t.Fatal("cancelled waiter released the owner")
	}
	gate.Unlock()
	if !gate.TryLock() {
		t.Fatal("owner did not release the gate")
	}
	gate.Unlock()
}

func TestGateWaitsUntilOwnerExits(t *testing.T) {
	gate := NewGate()
	gate.Lock()
	entered := make(chan struct{})
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		close(entered)
		err := gate.LockContext(ctx)
		if err == nil {
			gate.Unlock()
		}
		done <- err
	}()
	<-entered
	if gate.TryLock() {
		t.Fatal("busy gate admitted a second owner")
	}
	gate.Unlock()
	if err := <-done; err != nil {
		t.Fatalf("waiter failed: %v", err)
	}
}

func TestGateReleaseAfterOperationError(t *testing.T) {
	gate := NewGate()
	failure := errors.New("operation failed")
	operate := func() error {
		gate.Lock()
		defer gate.Unlock()
		return failure
	}
	if !errors.Is(operate(), failure) {
		t.Fatal("operation error was lost")
	}
	if !gate.TryLock() {
		t.Fatal("error leaked the gate")
	}
	gate.Unlock()
}

func TestUnlockFreeGatePreservesNonblockingAcquisition(t *testing.T) {
	gate := NewGate()
	gate.Unlock()
	if !gate.TryLock() {
		t.Fatal("free gate could not be acquired")
	}
	if gate.TryLock() {
		t.Fatal("gate admitted two owners")
	}
	gate.Unlock()
}

func TestGatesShareAnEscrowWithoutBlockingAnother(t *testing.T) {
	var gates Gates
	first := gates.For("escrow-a")
	first.Lock()
	if gates.For("escrow-a").TryLock() {
		t.Fatal("same escrow got independent gates")
	}
	other := gates.For("escrow-b")
	if !other.TryLock() {
		t.Fatal("unrelated escrow was blocked")
	}
	other.Unlock()
	first.Unlock()
}

func TestGatesConcurrentLookupHasOneOwner(t *testing.T) {
	var gates Gates
	owner := gates.For("escrow")
	owner.Lock()
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if gates.For("escrow").TryLock() {
				t.Error("lookup created another owner")
			}
		}()
	}
	workers.Wait()
	owner.Unlock()
}
