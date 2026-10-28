package escrow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/store"
)

func countingLookup(calls *int, info chain.EscrowInfo, err error) *fakeTxClient {
	return &fakeTxClient{getEscrowFn: func(_ context.Context, escrowID string) (chain.EscrowInfo, bool, error) {
		*calls++
		info.EscrowID = escrowID
		return info, err == nil, err
	}}
}

// Test flow:
//  1. Store twenty unresolved rows, one resolved row and one row gone from chain, with a lookup that answers chain epoch 7 and amount 5000.
//  2. Resolve once, then again.
//  3. Assert the first pass read sixteen rows and wrote what it read, the second read the remaining four, and neither read the resolved or the gone row.
func TestResolutionReadsAtMostSixteenUnresolvedRowsATick(t *testing.T) {
	testStore := newFakeStore()
	for index := range 20 {
		escrowID := fmt.Sprintf("%02d", index)
		testStore.devshards[escrowID] = store.DevshardRecord{EscrowID: escrowID, Model: "model-a", Active: true}
	}
	testStore.devshards["resolved"] = store.DevshardRecord{EscrowID: "resolved", Model: "model-a", Active: true, ChainEpoch: 6, Amount: 4000}
	testStore.devshards["gone"] = store.DevshardRecord{EscrowID: "gone", Model: "model-a", GoneFromChain: true}
	calls := 0
	cfg := config.Defaults()
	deps := testManagerDeps(t, testStore, &fakeTxClient{}, &fakeSnapshotSource{}, &cfg)
	deps.ChainFacts = countingLookup(&calls, chain.EscrowInfo{EpochIndex: 7, Balance: 5000}, nil)
	manager := mustManager(t, deps)
	devshards, _ := testStore.ListDevshards(context.Background())

	resolved := manager.resolveChainFacts(context.Background(), devshards)

	if calls != chainFactsPerTick {
		t.Fatalf("lookups on the first pass = %d, want %d", calls, chainFactsPerTick)
	}
	written := 0
	for _, record := range resolved {
		if record.ChainEpoch == 7 && record.Amount == 5000 && testStore.devshards[record.EscrowID].ChainEpoch == 7 {
			written++
		}
	}
	if written != chainFactsPerTick {
		t.Fatalf("rows resolved in memory and in the store = %d, want %d", written, chainFactsPerTick)
	}
	devshards, _ = testStore.ListDevshards(context.Background())
	manager.resolveChainFacts(context.Background(), devshards)
	if calls != 20 {
		t.Fatalf("lookups after two passes = %d, want 20", calls)
	}
	if testStore.devshards["resolved"].ChainEpoch != 6 || testStore.devshards["gone"].ChainEpoch != 0 {
		t.Fatalf("resolved row %+v and gone row %+v, want both untouched", testStore.devshards["resolved"], testStore.devshards["gone"])
	}
}

// Test flow:
//  1. Store one unresolved row with a lookup that fails.
//  2. Resolve.
//  3. Assert the row stays unresolved in memory and in the store, and nothing panics or errors.
func TestAFailedLookupLeavesTheRowUnresolved(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["1"] = store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true, RotationEpoch: 7}
	calls := 0
	cfg := config.Defaults()
	deps := testManagerDeps(t, testStore, &fakeTxClient{}, &fakeSnapshotSource{}, &cfg)
	deps.ChainFacts = countingLookup(&calls, chain.EscrowInfo{}, errors.New("chain unreachable"))
	manager := mustManager(t, deps)
	devshards, _ := testStore.ListDevshards(context.Background())

	resolved := manager.resolveChainFacts(context.Background(), devshards)

	if calls != 1 || resolved[0].ChainEpoch != 0 || testStore.devshards["1"].ChainEpoch != 0 {
		t.Fatalf("lookups %d, row %+v, want one lookup and the row unresolved", calls, resolved[0])
	}
}

// Test flow:
//  1. Build a manager without a chain-facts lookup over one unresolved row.
//  2. Resolve.
//  3. Assert the rows come back unchanged and the store saw no write.
func TestWithoutAChainFactsLookupNothingIsResolved(t *testing.T) {
	testStore := newFakeStore()
	testStore.calls = &callLog{}
	testStore.devshards["1"] = store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true}
	cfg := config.Defaults()
	manager := mustManager(t, testManagerDeps(t, testStore, &fakeTxClient{}, &fakeSnapshotSource{}, &cfg))
	devshards := []store.DevshardRecord{testStore.devshards["1"]}

	resolved := manager.resolveChainFacts(context.Background(), devshards)

	if resolved[0] != devshards[0] || len(testStore.calls.snapshot()) != 0 {
		t.Fatalf("resolveChainFacts() = %+v with store calls %v, want the row unchanged and no call", resolved[0], testStore.calls.snapshot())
	}
}

// Test flow:
//  1. Build a manager with a chain-facts lookup answering chain epoch 7 and amount 1000, and a chain client that creates escrow 42.
//  2. Create a regular escrow.
//  3. Assert the new row carries chain epoch 7 and amount 1000 as soon as the create returns.
func TestACreatedEscrowReadsItsChainFactsAtOnce(t *testing.T) {
	testStore := newFakeStore()
	calls := 0
	cfg := config.Defaults()
	deps := testManagerDeps(t, testStore, &fakeTxClient{createEscrowFn: workingCreateEscrowFn(42)}, &fakeSnapshotSource{}, &cfg)
	deps.ChainFacts = countingLookup(&calls, chain.EscrowInfo{EpochIndex: 7, Balance: 1000}, nil)
	manager := mustManager(t, deps)

	if _, err := manager.createFor(context.Background(), ModelConfig{ModelID: "model-a", Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}, roleRegular, 7, string(createdForBridge), chain.PhaseSnapshot{EpochIndex: 7, BlockHeight: 500}); err != nil {
		t.Fatalf("createFor() = %v, want nil", err)
	}

	if record := testStore.devshards["42"]; record.ChainEpoch != 7 || record.Amount != 1000 || calls != 1 {
		t.Fatalf("row %+v after %d lookups, want chain epoch 7 and amount 1000 after one", record, calls)
	}
}

// Test flow:
//  1. Store twenty unresolved rows whose lookups all fail except the last row's, which answers chain epoch 7 and amount 5000.
//  2. Resolve once and assert the first sixteen rows were read and the last row is still unresolved.
//  3. Resolve again and assert the walk went on past the sixteenth row, so the last row resolved and every row was read at least once.
func TestResolutionWalksPastRowsThatNeverResolve(t *testing.T) {
	testStore := newFakeStore()
	for index := range 20 {
		escrowID := fmt.Sprintf("%02d", index)
		testStore.devshards[escrowID] = store.DevshardRecord{EscrowID: escrowID, Model: "model-a", Active: true}
	}
	lookups := map[string]int{}
	cfg := config.Defaults()
	deps := testManagerDeps(t, testStore, &fakeTxClient{}, &fakeSnapshotSource{}, &cfg)
	deps.ChainFacts = &fakeTxClient{getEscrowFn: func(_ context.Context, escrowID string) (chain.EscrowInfo, bool, error) {
		lookups[escrowID]++
		if escrowID != "19" {
			return chain.EscrowInfo{}, false, errors.New("chain unreachable")
		}
		return chain.EscrowInfo{EscrowID: escrowID, EpochIndex: 7, Balance: 5000}, true, nil
	}}
	manager := mustManager(t, deps)
	devshards, _ := testStore.ListDevshards(context.Background())

	manager.resolveChainFacts(context.Background(), devshards)

	if len(lookups) != chainFactsPerTick || lookups["15"] != 1 || lookups["19"] != 0 {
		t.Fatalf("lookups after one pass = %v, want rows 00 to 15 once each", lookups)
	}
	devshards, _ = testStore.ListDevshards(context.Background())
	manager.resolveChainFacts(context.Background(), devshards)
	if record := testStore.devshards["19"]; record.ChainEpoch != 7 || record.Amount != 5000 {
		t.Fatalf("row 19 = %+v after two passes, want chain epoch 7 and amount 5000", record)
	}
	if len(lookups) != 20 {
		t.Fatalf("rows read after two passes = %d, want all 20", len(lookups))
	}
}

// Test flow:
//  1. Store one unresolved row whose lookup fails, with a recording narrator.
//  2. Resolve three times, then let the lookup answer and resolve once more.
//  3. Assert the failure was narrated once, with its reason, and the resolution added no line.
func TestAnUnresolvedRowIsNarratedOncePerEpisode(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["1"] = store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true}
	chainDown := true
	narrator := &recordingLifecycleNarrator{}
	cfg := config.Defaults()
	deps := testManagerDeps(t, testStore, &fakeTxClient{}, &fakeSnapshotSource{}, &cfg)
	deps.Narrator = narrator
	deps.ChainFacts = &fakeTxClient{getEscrowFn: func(_ context.Context, escrowID string) (chain.EscrowInfo, bool, error) {
		if chainDown {
			return chain.EscrowInfo{}, false, errors.New("chain unreachable")
		}
		return chain.EscrowInfo{EscrowID: escrowID, EpochIndex: 7, Balance: 5000}, true, nil
	}}
	manager := mustManager(t, deps)

	for range 3 {
		devshards, _ := testStore.ListDevshards(context.Background())
		manager.resolveChainFacts(context.Background(), devshards)
	}
	chainDown = false
	devshards, _ := testStore.ListDevshards(context.Background())
	manager.resolveChainFacts(context.Background(), devshards)

	want := []string{"chain facts unresolved 1: lookup_failed"}
	if got := narrator.recorded(); !slices.Equal(got, want) || testStore.devshards["1"].ChainEpoch != 7 {
		t.Fatalf("narration = %v with row %+v, want %v and the row resolved", got, testStore.devshards["1"], want)
	}
}

// Test flow:
//  1. Build a manager without a narrator over one row the chain no longer holds and one whose answer carries no epoch.
//  2. Resolve.
//  3. Assert both rows stay unresolved and nothing panics.
func TestARowTheChainDoesNotHoldOrStampsNoEpochStaysUnresolved(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["absent"] = store.DevshardRecord{EscrowID: "absent", Model: "model-a"}
	testStore.devshards["unstamped"] = store.DevshardRecord{EscrowID: "unstamped", Model: "model-a", Active: true}
	cfg := config.Defaults()
	deps := testManagerDeps(t, testStore, &fakeTxClient{}, &fakeSnapshotSource{}, &cfg)
	deps.ChainFacts = &fakeTxClient{getEscrowFn: func(_ context.Context, escrowID string) (chain.EscrowInfo, bool, error) {
		return chain.EscrowInfo{EscrowID: escrowID, Balance: 5000}, escrowID == "unstamped", nil
	}}
	manager := mustManager(t, deps)
	devshards, _ := testStore.ListDevshards(context.Background())

	manager.resolveChainFacts(context.Background(), devshards)

	if testStore.devshards["absent"].ChainEpoch != 0 || testStore.devshards["unstamped"].Amount != 0 {
		t.Fatalf("rows %+v and %+v, want both unresolved", testStore.devshards["absent"], testStore.devshards["unstamped"])
	}
}
