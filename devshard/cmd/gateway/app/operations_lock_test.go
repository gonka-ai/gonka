package app

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/escrow"
)

const (
	racingOperatorRounds = 50
	goroutineDumpBytes   = 1 << 20
)

// Test flow:
//  1. Register escrow 42 active.
//  2. Each round, run an operator Activate and an operator Deactivate of escrow 42 at once.
//  3. Assert after every round that escrow 42 routes exactly when its row is active.
func TestConcurrentActivateAndDeactivateLeaveTheRowAndRoutingAgreeing(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DEVSHARD_IMPORT_KEY", "0x01")
	records := openedStore(t)
	escrows := servingRegistry(t)
	operator := &operations{store: records, escrows: escrows, storageDir: t.TempDir()}
	if err := operator.AddDevshard(ctx, api.AddDevshardRequest{EscrowID: "42", Model: "model-a", PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Activate: true}); err != nil {
		t.Fatalf("AddDevshard(active) = %v", err)
	}

	for round := range racingOperatorRounds {
		var calls sync.WaitGroup
		calls.Go(func() {
			if err := operator.Activate(ctx, "42"); err != nil {
				t.Errorf("Activate = %v", err)
			}
		})
		calls.Go(func() {
			if err := operator.Deactivate(ctx, "42"); err != nil {
				t.Errorf("Deactivate = %v", err)
			}
		})
		calls.Wait()

		active := storedDevshard(t, records, "42").Active
		if _, routable := escrows.Routable("42"); routable != active {
			t.Fatalf("round %d: escrow 42's row active = %v but routable = %v", round, active, routable)
		}
	}
}

func goroutineRunning(frames ...string) bool {
	dump := make([]byte, goroutineDumpBytes)
	for goroutine := range strings.SplitSeq(string(dump[:runtime.Stack(dump, true)]), "\n\n") {
		missing := func(frame string) bool { return !strings.Contains(goroutine, frame) }
		if !slices.ContainsFunc(frames, missing) {
			return true
		}
	}
	return false
}

func awaitBlockedInLockWrites(t *testing.T, caller string, finished <-chan error, unlock func()) {
	t.Helper()
	for !goroutineRunning(caller, "(*operations).lockWrites", "sync.(*Mutex).Lock") {
		select {
		case err := <-finished:
			unlock()
			t.Fatalf("%s finished while another write held escrow 42's lock: %v", caller, err)
		default:
			runtime.Gosched()
		}
	}
}

func awaitRunningIn(t *testing.T, frame string, finished <-chan error, release func()) {
	t.Helper()
	for !goroutineRunning(frame) {
		select {
		case err := <-finished:
			release()
			t.Fatalf("the call finished before reaching %s: %v", frame, err)
		default:
			runtime.Gosched()
		}
	}
}

type lockCheckingSettler struct {
	operator *operations
	lockHeld bool
}

func (s *lockCheckingSettler) CreateEscrow(context.Context, escrow.ModelConfig) (chain.CreateEscrowResult, error) {
	return chain.CreateEscrowResult{}, nil
}

func (s *lockCheckingSettler) Settle(_ context.Context, escrowID string, _ bool) (chain.SettleEscrowResult, error) {
	lock, stored := s.operator.writes.Load(escrowID)
	if !stored {
		return chain.SettleEscrowResult{}, nil
	}
	writes := lock.(*sync.Mutex)
	s.lockHeld = !writes.TryLock()
	if !s.lockHeld {
		writes.Unlock()
	}
	return chain.SettleEscrowResult{}, nil
}

// Test flow:
//  1. Store and publish escrow 42 behind a settler that tries escrow 42's write lock when the chain settlement begins.
//  2. Settle escrow 42.
//  3. Assert the settle succeeds and the lock was held while the chain settlement ran.
func TestASettleHoldsTheEscrowsLockAroundTheChainSettlement(t *testing.T) {
	records, escrows := settleableEscrow(t)
	settler := &lockCheckingSettler{}
	operator := &operations{store: records, escrows: escrows, manager: settler}
	settler.operator = operator

	if _, err := operator.Settle(context.Background(), "42", false); err != nil {
		t.Fatalf("Settle = %v", err)
	}

	if !settler.lockHeld {
		t.Error("the chain settlement ran without escrow 42's write lock held")
	}
}

// Test flow:
//  1. Store and publish escrow 42 and take its write lock as an operator Activate does.
//  2. Start a settle of escrow 42 and wait until it blocks on the lock, failing if it finishes first.
//  3. Assert escrow 42 still routes, then release the lock.
//  4. Assert the settle succeeds and the escrow no longer routes.
func TestASettleWaitsForAnotherWriteHoldingTheEscrowsLock(t *testing.T) {
	records, escrows := settleableEscrow(t)
	operator := &operations{store: records, escrows: escrows, manager: &settleObserver{escrows: escrows}}
	unlock := operator.lockWrites("42")

	settled := make(chan error, 1)
	go func() {
		_, err := operator.Settle(context.Background(), "42", false)
		settled <- err
	}()
	awaitBlockedInLockWrites(t, "(*operations).Settle", settled, unlock)
	_, routable := escrows.Routable("42")
	unlock()
	err := <-settled

	if !routable {
		t.Error("escrow 42 stopped routing while another write held its lock")
	}
	if err != nil {
		t.Errorf("Settle = %v", err)
	}
	if _, routable := escrows.Routable("42"); routable {
		t.Error("escrow 42 still routes after the settle")
	}
}
