package escrow

import (
	"context"
	"errors"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/store"
)

// Test flow:
//  1. Store one active devshard record and a tx client whose GetEscrow reports the escrow not found.
//  2. Call OnEscrowMissing for that escrow ID.
//  3. Assert GetEscrow was not called synchronously inside the hook.
//  4. Call tick and assert it succeeds.
//  5. Assert the stored record is now inactive and marked gone from chain.
func TestOnEscrowMissingDeactivatesTheEscrowOnTheNextTick(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["1"] = store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true}
	log := &callLog{}
	testStore.calls = log
	txClient := &fakeTxClient{
		calls: log,
		getEscrowFn: func(ctx context.Context, escrowID string) (chain.EscrowInfo, bool, error) {
			return chain.EscrowInfo{}, false, nil
		},
	}
	cfg := config.Defaults()
	cfg.Rotation.Enabled = false
	m := mustManager(t, testManagerDeps(t, testStore, txClient, &fakeSnapshotSource{}, &cfg))

	m.OnEscrowMissing("1")
	if hasCall(log.snapshot(), "GetEscrow") {
		t.Fatal("GetEscrow ran inside the hook: the report path reached the chain")
	}
	if err := m.tick(context.Background()); err != nil {
		t.Fatalf("tick() = %v, want nil", err)
	}

	if got := testStore.devshards["1"]; got.Active || !got.GoneFromChain {
		t.Fatalf("devshard = %+v, want inactive and gone from chain: a host reported the escrow gone and the chain confirmed it", got)
	}
}

// Test flow:
//  1. Store one active devshard record and a tx client whose GetEscrow reports the escrow not found.
//  2. Call OnEscrowMissing once, then call tick twice.
//  3. Count how many times "GetEscrow" appears in the call log.
//  4. Assert GetEscrow ran exactly once, proving the missing mark is consumed by the first tick and not rechecked by the second.
func TestOnEscrowMissingChecksEachMarkOnce(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["1"] = store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true}
	log := &callLog{}
	testStore.calls = log
	txClient := &fakeTxClient{
		calls: log,
		getEscrowFn: func(ctx context.Context, escrowID string) (chain.EscrowInfo, bool, error) {
			return chain.EscrowInfo{}, false, nil
		},
	}
	cfg := config.Defaults()
	cfg.Rotation.Enabled = false
	m := mustManager(t, testManagerDeps(t, testStore, txClient, &fakeSnapshotSource{}, &cfg))

	m.OnEscrowMissing("1")
	for range 2 {
		if err := m.tick(context.Background()); err != nil {
			t.Fatalf("tick() = %v, want nil", err)
		}
	}

	lookups := 0
	for _, name := range log.snapshot() {
		if name == "GetEscrow" {
			lookups++
		}
	}
	if lookups != 1 {
		t.Fatalf("GetEscrow ran %d times, want exactly 1: the mark outlived the tick that drained it", lookups)
	}
}

// Test flow:
//  1. Store one active devshard record and a tx client whose GetEscrow reports the escrow not found.
//  2. Call TriggerEscrowCheck for that escrow ID and assert it returns no error.
//  3. Assert the stored record's Active flag is now false.
func TestTriggerEscrowCheckNotFoundDeactivates(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["1"] = store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true}
	txClient := &fakeTxClient{
		getEscrowFn: func(ctx context.Context, escrowID string) (chain.EscrowInfo, bool, error) {
			return chain.EscrowInfo{}, false, nil
		},
	}
	m := &Manager{tx: txClient, store: testStore, settlementSource: &fakeSettlementSource{}}

	if err := m.TriggerEscrowCheck(context.Background(), "1"); err != nil {
		t.Fatalf("TriggerEscrowCheck() = %v, want nil", err)
	}
	if got := testStore.devshards["1"]; got.Active {
		t.Fatal("devshard.Active = true, want false (chain confirmed the escrow does not exist)")
	}
}

// Test flow:
//  1. Store one active devshard record and a tx client whose GetEscrow reports the escrow not found.
//  2. Assert the `fakeSettlementSource` accepts a nonce commit as a precondition.
//  3. Call TriggerEscrowCheck for that escrow ID and assert it returns no error.
//  4. Assert the settlement source no longer accepts a nonce commit on that escrow.
func TestTriggerEscrowCheckNotFoundStopsTraffic(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["1"] = store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true}
	routing := &fakeSettlementSource{}
	txClient := &fakeTxClient{
		getEscrowFn: func(ctx context.Context, escrowID string) (chain.EscrowInfo, bool, error) {
			return chain.EscrowInfo{}, false, nil
		},
	}
	m := &Manager{tx: txClient, store: testStore, settlementSource: routing}
	if !routing.commit() {
		t.Fatal("precondition: the escrow must accept a nonce commit before the check runs")
	}

	if err := m.TriggerEscrowCheck(context.Background(), "1"); err != nil {
		t.Fatalf("TriggerEscrowCheck() = %v, want nil", err)
	}

	if routing.commit() {
		t.Fatal("an escrow the chain confirmed absent still accepted a nonce commit")
	}
}

// Test flow:
//  1. Store one active devshard record and a tx client whose GetEscrow reports the escrow found with a balance.
//  2. Call TriggerEscrowCheck for that escrow ID and assert it returns no error.
//  3. Assert the stored record's Active flag stays true.
//  4. Assert the call log contains no "MarkDevshardGoneFromChain" call.
func TestTriggerEscrowCheckFoundKeepsActive(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["1"] = store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true}
	log := &callLog{}
	testStore.calls = log
	txClient := &fakeTxClient{
		getEscrowFn: func(ctx context.Context, escrowID string) (chain.EscrowInfo, bool, error) {
			return chain.EscrowInfo{EscrowID: escrowID, Balance: 500}, true, nil
		},
	}
	m := &Manager{tx: txClient, store: testStore, settlementSource: &fakeSettlementSource{}}

	if err := m.TriggerEscrowCheck(context.Background(), "1"); err != nil {
		t.Fatalf("TriggerEscrowCheck() = %v, want nil", err)
	}
	if got := testStore.devshards["1"]; !got.Active {
		t.Fatal("devshard.Active = false, want true (escrow found, no reason to deactivate)")
	}
	for _, name := range log.snapshot() {
		if name == "MarkDevshardGoneFromChain" {
			t.Fatal("MarkDevshardGoneFromChain called, want no deactivation when the escrow is found")
		}
	}
}

// Test flow:
//  1. Store one active devshard record and a tx client whose GetEscrow returns an error.
//  2. Call TriggerEscrowCheck for that escrow ID.
//  3. Assert the returned error wraps the chain error.
//  4. Assert the stored record's Active flag stays true and no "MarkDevshardGoneFromChain" call was logged.
func TestTriggerEscrowCheckChainErrorKeepsActive(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["1"] = store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true}
	log := &callLog{}
	testStore.calls = log
	chainErr := errors.New("endpoint unreachable")
	txClient := &fakeTxClient{
		getEscrowFn: func(ctx context.Context, escrowID string) (chain.EscrowInfo, bool, error) {
			return chain.EscrowInfo{}, false, chainErr
		},
	}
	m := &Manager{tx: txClient, store: testStore, settlementSource: &fakeSettlementSource{}}

	err := m.TriggerEscrowCheck(context.Background(), "1")
	if err == nil || !errors.Is(err, chainErr) {
		t.Fatalf("TriggerEscrowCheck() = %v, want wrapped %v", err, chainErr)
	}
	if got := testStore.devshards["1"]; !got.Active {
		t.Fatal("devshard.Active = false, want true (a lookup failure is ambiguity, never a reason to deactivate)")
	}
	for _, name := range log.snapshot() {
		if name == "MarkDevshardGoneFromChain" {
			t.Fatal("MarkDevshardGoneFromChain called, want no deactivation on chain error")
		}
	}
}

// Test flow:
//  1. Store one active devshard record and a tx client whose GetEscrow blocks on a release channel before reporting not found.
//  2. Launch 10 concurrent calls to TriggerEscrowCheck for the same escrow ID.
//  3. Drain the 9 deduped callers that return without touching the chain, then close the release channel and drain the winner.
//  4. Assert every call returned no error.
//  5. Count "GetEscrow" and "MarkDevshardGoneFromChain" in the call log and assert each happened exactly once.
func TestTriggerEscrowCheckDedupesConcurrentCallers(t *testing.T) {
	const callerCount = 10
	testStore := newFakeStore()
	testStore.devshards["1"] = store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true}
	log := &callLog{}
	testStore.calls = log

	release := make(chan struct{})
	txClient := &fakeTxClient{
		calls: log,
		getEscrowFn: func(ctx context.Context, escrowID string) (chain.EscrowInfo, bool, error) {
			<-release
			return chain.EscrowInfo{}, false, nil
		},
	}
	m := &Manager{tx: txClient, store: testStore, settlementSource: &fakeSettlementSource{}}

	done := make(chan struct{}, callerCount)
	for range callerCount {
		go func() {
			if err := m.TriggerEscrowCheck(context.Background(), "1"); err != nil {
				t.Errorf("TriggerEscrowCheck() = %v, want nil", err)
			}
			done <- struct{}{}
		}()
	}

	for range callerCount - 1 {
		<-done
	}
	close(release)
	<-done

	getEscrowCalls, deactivateCalls := 0, 0
	for _, name := range log.snapshot() {
		switch name {
		case "GetEscrow":
			getEscrowCalls++
		case "MarkDevshardGoneFromChain":
			deactivateCalls++
		}
	}
	if getEscrowCalls != 1 {
		t.Fatalf("GetEscrow called %d times, want exactly 1", getEscrowCalls)
	}
	if deactivateCalls != 1 {
		t.Fatalf("MarkDevshardGoneFromChain called %d times, want exactly 1", deactivateCalls)
	}
}
