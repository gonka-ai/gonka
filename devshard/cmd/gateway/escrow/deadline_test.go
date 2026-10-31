package escrow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/store"
	"devshard/signing"
)

// Test flow:
//  1. Table-driven over the chain epoch, the label, the effective and latest epochs, the height and the switch height, with a 600-block margin.
//  2. Assert deadlineAt reports whether the deadline is known, passed or inside the margin, and the settle-by height.
func TestTheDeadlineReadsTheChainEpochAgainstTheEffectiveEpoch(t *testing.T) {
	lastEpoch := chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: 2100, EpochSwitchBlockHeight: 3100}
	insideMargin := chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: 2600, EpochSwitchBlockHeight: 3100}
	testCases := []struct {
		name     string
		record   store.DevshardRecord
		snapshot chain.PhaseSnapshot
		want     settleDeadline
	}{
		{name: "the chain epoch is the effective one", record: store.DevshardRecord{ChainEpoch: 8}, snapshot: insideMargin, want: settleDeadline{known: true}},
		{name: "last epoch, outside the margin", record: store.DevshardRecord{ChainEpoch: 7}, snapshot: lastEpoch, want: settleDeadline{known: true, settleBy: 3100}},
		{name: "last epoch, inside the margin", record: store.DevshardRecord{ChainEpoch: 7}, snapshot: insideMargin, want: settleDeadline{known: true, inMargin: true, settleBy: 3100}},
		{name: "two epochs behind has passed", record: store.DevshardRecord{ChainEpoch: 6}, snapshot: lastEpoch, want: settleDeadline{known: true, passed: true, inMargin: true}},
		{name: "an unresolved row reads its label less one", record: store.DevshardRecord{RotationEpoch: 8}, snapshot: insideMargin, want: settleDeadline{known: true, inMargin: true, settleBy: 3100}},
		{name: "a resolved chain epoch wins over the label", record: store.DevshardRecord{ChainEpoch: 7, RotationEpoch: 10}, snapshot: insideMargin, want: settleDeadline{known: true, inMargin: true, settleBy: 3100}},
		{name: "an unknown effective epoch reads the latest", record: store.DevshardRecord{ChainEpoch: 7}, snapshot: chain.PhaseSnapshot{EpochIndex: 8, BlockHeight: 2050, EpochSwitchBlockHeight: 2100}, want: settleDeadline{known: true, inMargin: true, settleBy: 2100}},
		{name: "no switch height is inside the margin", record: store.DevshardRecord{ChainEpoch: 7}, snapshot: chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: 2100}, want: settleDeadline{known: true, inMargin: true}},
		{name: "a row with no epoch at all", record: store.DevshardRecord{}, snapshot: insideMargin, want: settleDeadline{}},
		{name: "a cold start", record: store.DevshardRecord{ChainEpoch: 7}, snapshot: chain.PhaseSnapshot{}, want: settleDeadline{}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := deadlineAt(testCase.record, testCase.snapshot, 600); got != testCase.want {
				t.Fatalf("deadlineAt(%s) = %+v, want %+v", testCase.name, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Table-driven over the blocks around the switch S into epoch 8, with the snapshot each block yields: at S it already names the next switch.
//  2. Assert a row of chain epoch 7 enters no margin at S or S+1, and a row of chain epoch 6 is inside its margin at S-1 and passed at S.
func TestTheDeadlineAroundTheSwitchBlockReadsTheNextSwitch(t *testing.T) {
	beforeSwitch := chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 7, BlockHeight: 2099, EpochSwitchBlockHeight: 2100}
	atSwitch := chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: 2100, EpochSwitchBlockHeight: 3100}
	afterSwitch := chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: 2101, EpochSwitchBlockHeight: 3100}
	testCases := []struct {
		name       string
		chainEpoch uint64
		snapshot   chain.PhaseSnapshot
		want       settleDeadline
	}{
		{name: "epoch 7 at S-1", chainEpoch: 7, snapshot: beforeSwitch, want: settleDeadline{known: true}},
		{name: "epoch 7 at S", chainEpoch: 7, snapshot: atSwitch, want: settleDeadline{known: true, settleBy: 3100}},
		{name: "epoch 7 at S+1", chainEpoch: 7, snapshot: afterSwitch, want: settleDeadline{known: true, settleBy: 3100}},
		{name: "epoch 6 at S-1", chainEpoch: 6, snapshot: beforeSwitch, want: settleDeadline{known: true, inMargin: true, settleBy: 2100}},
		{name: "epoch 6 at S", chainEpoch: 6, snapshot: atSwitch, want: settleDeadline{known: true, passed: true, inMargin: true}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := deadlineAt(store.DevshardRecord{ChainEpoch: testCase.chainEpoch}, testCase.snapshot, 600); got != testCase.want {
				t.Fatalf("deadlineAt(%s) = %+v, want %+v", testCase.name, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Sort a deadline with no settle-by height, a later and an earlier settle-by height, and a passed deadline by their order.
//  2. Assert the passed deadline comes first, then the earlier height, the later one, and the one without a height last.
func TestDeadlinesOrderPassedFirstThenBySettleByHeight(t *testing.T) {
	passed := settleDeadline{known: true, passed: true, inMargin: true}
	earlier := settleDeadline{known: true, settleBy: 2100}
	later := settleDeadline{known: true, settleBy: 3100}
	unknown := settleDeadline{known: true}
	deadlines := []settleDeadline{unknown, later, earlier, passed}

	slices.SortFunc(deadlines, func(left, right settleDeadline) int { return cmp.Compare(left.order(), right.order()) })

	if want := []settleDeadline{passed, earlier, later, unknown}; !slices.Equal(deadlines, want) {
		t.Fatalf("deadlines sorted by order() = %+v, want %+v", deadlines, want)
	}
}

func deadlineManager(t *testing.T, testStore *fakeStore, modelsJSON string) (*Manager, *recordingLifecycleNarrator) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Rotation.SettlementEnabled = true
	cfg.Rotation.ModelsJSON = modelsJSON
	deps := testManagerDeps(t, testStore, &fakeTxClient{}, &fakeSnapshotSource{}, &cfg)
	deps.ChainFacts = &fakeTxClient{}
	narrator := &recordingLifecycleNarrator{}
	deps.Narrator = narrator
	return mustManager(t, deps), narrator
}

// Test flow:
//  1. Store, all of chain epoch 7 inside the margin of the switch into epoch 9: a serving row, a serving row of a model with settlement off, an operator-deactivated row, a row gone from chain; and a serving row of chain epoch 8.
//  2. Run the deadline pass twice.
//  3. Assert only the first row was parked and narrated as reached; the settlement-off and deactivated rows were narrated once each and left as they were; the gone row and the chain-epoch-8 row were untouched.
func TestTheDeadlineParksWhatMaySettleAndNarratesTheRest(t *testing.T) {
	testStore := newFakeStore()
	rows := []store.DevshardRecord{
		{EscrowID: "1", Model: "model-a", Active: true, ChainEpoch: 7},
		{EscrowID: "2", Model: "model-b", Active: true, ChainEpoch: 7},
		{EscrowID: "3", Model: "model-a", ChainEpoch: 7},
		{EscrowID: "4", Model: "model-a", ChainEpoch: 7, GoneFromChain: true},
		{EscrowID: "5", Model: "model-a", Active: true, ChainEpoch: 8},
	}
	for _, row := range rows {
		testStore.devshards[row.EscrowID] = row
	}
	manager, narrator := deadlineManager(t, testStore, `[{"model_id":"model-b","amount":1000,"settlement_enabled":false}]`)
	snapshot := chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: 2600, EpochSwitchBlockHeight: 3100}

	updated, err := manager.parkAtDeadline(context.Background(), snapshot, rows)
	if err != nil {
		t.Fatalf("parkAtDeadline() = %v, want nil", err)
	}
	if _, err := manager.parkAtDeadline(context.Background(), snapshot, updated); err != nil {
		t.Fatalf("parkAtDeadline() again = %v, want nil", err)
	}

	assertParked(t, testStore, "1")
	if !slices.ContainsFunc(updated, func(record store.DevshardRecord) bool {
		return record.EscrowID == "1" && !record.Active && record.SettlementPending
	}) {
		t.Fatalf("parkAtDeadline() rows = %+v, want row 1 parked in the returned slice", updated)
	}
	if !testStore.devshards["2"].Active || !testStore.devshards["5"].Active || testStore.devshards["3"].SettlementPending || testStore.devshards["4"].SettlementPending {
		t.Fatalf("rows 2-5 = %+v %+v %+v %+v, want untouched", testStore.devshards["2"], testStore.devshards["3"], testStore.devshards["4"], testStore.devshards["5"])
	}
	want := []string{
		"deadline reached 1 epoch 7 settle by 3100",
		"deadline unsettled 2 settle by 3100: settlement_disabled",
		"deadline unsettled 3 settle by 3100: operator_deactivated",
	}
	recorded := slices.DeleteFunc(narrator.recorded(), func(line string) bool { return line == "parked 1" })
	if !slices.Equal(recorded, want) {
		t.Fatalf("narration = %v, want %v", recorded, want)
	}
}

// Test flow:
//  1. Store a parked row of chain epoch 6 at effective epoch 8, two epochs behind.
//  2. Run the deadline pass.
//  3. Assert it was narrated as deadline_passed and left parked for settlePending to drop.
func TestAPassedDeadlineIsNarrated(t *testing.T) {
	testStore := newFakeStore()
	row := store.DevshardRecord{EscrowID: "1", Model: "model-a", SettlementPending: true, ChainEpoch: 6}
	testStore.devshards["1"] = row
	manager, narrator := deadlineManager(t, testStore, "")

	if _, err := manager.parkAtDeadline(context.Background(), chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: 2100, EpochSwitchBlockHeight: 3100}, []store.DevshardRecord{row}); err != nil {
		t.Fatalf("parkAtDeadline() = %v, want nil", err)
	}

	if recorded := narrator.recorded(); !slices.Equal(recorded, []string{"deadline unsettled 1 settle by 0: deadline_passed"}) {
		t.Fatalf("narration = %v, want one deadline_passed line", recorded)
	}
}

// Test flow:
//  1. Store a row an operator deactivated after the tick read it as serving, inside its margin.
//  2. Run the deadline pass over the stale serving copy.
//  3. Assert the stored row stays inactive and unparked, and nothing was narrated as reached.
func TestTheDeadlineDoesNotParkARowAnOperatorDeactivatedMidTick(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["1"] = store.DevshardRecord{EscrowID: "1", Model: "model-a", ChainEpoch: 7}
	stale := store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true, ChainEpoch: 7}
	manager, narrator := deadlineManager(t, testStore, "")
	snapshot := chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: 2600, EpochSwitchBlockHeight: 3100}

	updated, err := manager.parkAtDeadline(context.Background(), snapshot, []store.DevshardRecord{stale})
	if err != nil {
		t.Fatalf("parkAtDeadline() = %v, want nil", err)
	}

	if stored := testStore.devshards["1"]; stored.SettlementPending || stored.Active {
		t.Fatalf("stored row = %+v, want still deactivated and unparked", stored)
	}
	if updated[0].SettlementPending || len(narrator.recorded()) != 0 {
		t.Fatalf("parkAtDeadline() row = %+v narration = %v, want no park and no narration", updated[0], narrator.recorded())
	}
}

// Test flow:
//  1. Store a serving row inside its margin; build one manager without a chain-facts lookup, and run another over a snapshot with no epoch.
//  2. Run the deadline pass on each.
//  3. Assert neither touched the row.
func TestTheDeadlineNeedsChainFactsAndAnEpoch(t *testing.T) {
	row := store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true, ChainEpoch: 7}
	inside := chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: 2600, EpochSwitchBlockHeight: 3100}

	withoutFacts := newFakeStore()
	withoutFacts.devshards["1"] = row
	cfg := config.Defaults()
	manager := mustManager(t, testManagerDeps(t, withoutFacts, &fakeTxClient{}, &fakeSnapshotSource{}, &cfg))
	if _, err := manager.parkAtDeadline(context.Background(), inside, []store.DevshardRecord{row}); err != nil || !withoutFacts.devshards["1"].Active {
		t.Fatalf("parkAtDeadline() without chain facts = %v, row %+v, want nil and still serving", err, withoutFacts.devshards["1"])
	}

	coldStart := newFakeStore()
	coldStart.devshards["1"] = row
	manager, _ = deadlineManager(t, coldStart, "")
	if _, err := manager.parkAtDeadline(context.Background(), chain.PhaseSnapshot{}, []store.DevshardRecord{row}); err != nil || !coldStart.devshards["1"].Active {
		t.Fatalf("parkAtDeadline() on a cold start = %v, row %+v, want nil and still serving", err, coldStart.devshards["1"])
	}
}

// recordingSettlementSource records which escrows reached Finalize and which the busy check was asked about.
type recordingSettlementSource struct {
	*fakeSettlementSource
	mu          sync.Mutex
	finalized   []string
	busyAskedOf []string
}

func (source *recordingSettlementSource) Finalize(ctx context.Context, escrowID string) error {
	source.mu.Lock()
	source.finalized = append(source.finalized, escrowID)
	source.mu.Unlock()
	return source.fakeSettlementSource.Finalize(ctx, escrowID)
}

func (source *recordingSettlementSource) IsBusy(escrowID string) bool {
	source.mu.Lock()
	source.busyAskedOf = append(source.busyAskedOf, escrowID)
	source.mu.Unlock()
	return source.fakeSettlementSource.IsBusy(escrowID)
}

// Test flow:
//  1. Store five parked rows of chain epoch 8 outside any margin and two of chain epoch 7 inside the margin of the switch into epoch 9, every escrow still busy.
//  2. Settle the pending rows once.
//  3. Assert the two inside the margin were finalized despite being busy, and the four earliest of the other five by deadline order were tried and deferred as busy, which is no tick failure: the budget counted only them.
func TestPendingSettlesRunWithTheMarginForcedAndUnbudgeted(t *testing.T) {
	testStore := newFakeStore()
	devshards := make([]store.DevshardRecord, 0, 7)
	for _, escrowID := range []string{"10", "11", "12", "13", "14"} {
		devshards = append(devshards, store.DevshardRecord{EscrowID: escrowID, PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", SettlementPending: true, ChainEpoch: 8})
	}
	for _, escrowID := range []string{"31", "30"} {
		devshards = append(devshards, store.DevshardRecord{EscrowID: escrowID, PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", SettlementPending: true, ChainEpoch: 7})
	}
	for _, record := range devshards {
		testStore.devshards[record.EscrowID] = record
	}
	source := &recordingSettlementSource{fakeSettlementSource: &fakeSettlementSource{busy: true}}
	manager := &Manager{
		tx:               settlingTxClient(),
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: source,
		config:           holderWithSettlementEnabled(true),
		chainFacts:       &fakeTxClient{},
		now:              time.Now,
	}
	err := manager.settlePending(context.Background(), snapshotBeforeTheSwitch, devshards)

	if err != nil {
		t.Fatalf("settlePending() = %v, want nil: deferred busy settles are no tick failure", err)
	}
	slices.Sort(source.finalized)
	if !slices.Equal(source.finalized, []string{"30", "31"}) {
		t.Fatalf("finalized %v, want [30 31]: the rows inside the margin, forced", source.finalized)
	}
	slices.Sort(source.busyAskedOf)
	if !slices.Equal(source.busyAskedOf, []string{"10", "11", "12", "13"}) {
		t.Fatalf("busy checks for %v, want [10 11 12 13]: the budget goes to the rows outside the margin in deadline order, and only they count against it", source.busyAskedOf)
	}
}

// Test flow:
//  1. Store a settlement-off row and an operator-deactivated row inside their margin, and run the deadline pass twice.
//  2. Assert each was counted once under its model and reason.
func TestEveryUnsettledDeadlineIsCountedOnce(t *testing.T) {
	testStore := newFakeStore()
	rows := []store.DevshardRecord{
		{EscrowID: "2", Model: "model-b", Active: true, ChainEpoch: 7},
		{EscrowID: "3", Model: "model-a", ChainEpoch: 7},
	}
	for _, row := range rows {
		testStore.devshards[row.EscrowID] = row
	}
	manager, _ := deadlineManager(t, testStore, `[{"model_id":"model-b","amount":1000,"settlement_enabled":false}]`)
	snapshot := chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: 2600, EpochSwitchBlockHeight: 3100}

	for range 2 {
		if _, err := manager.parkAtDeadline(context.Background(), snapshot, rows); err != nil {
			t.Fatalf("parkAtDeadline() = %v, want nil", err)
		}
	}

	want := map[DeadlineUnsettled]uint64{
		{Model: "model-b", Reason: "settlement_disabled"}:  1,
		{Model: "model-a", Reason: "operator_deactivated"}: 1,
	}
	if counts := manager.DeadlineUnsettledCounts(); !maps.Equal(counts, want) {
		t.Fatalf("DeadlineUnsettledCounts() = %v, want %v", counts, want)
	}
}

// Test flow:
//  1. Store an inactive, unparked row of chain epoch 7 inside its margin, on a manager whose signer cannot resolve the row's key.
//  2. Run the deadline pass.
//  3. Assert it was narrated as key_missing, not as an operator's deactivation.
func TestADeactivatedRowWhoseKeyIsMissingIsNarratedKeyMissing(t *testing.T) {
	testStore := newFakeStore()
	row := store.DevshardRecord{EscrowID: "3", Model: "model-a", ChainEpoch: 7, PrivateKeyEnv: "GONE_KEY"}
	testStore.devshards["3"] = row
	manager, narrator := deadlineManager(t, testStore, "")
	manager.signer = &fakeSignerSource{err: errors.New("private key GONE_KEY is not set")}
	snapshot := chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: 2600, EpochSwitchBlockHeight: 3100}

	if _, err := manager.parkAtDeadline(context.Background(), snapshot, []store.DevshardRecord{row}); err != nil {
		t.Fatalf("parkAtDeadline() = %v, want nil", err)
	}

	if recorded := narrator.recorded(); !slices.Equal(recorded, []string{"deadline unsettled 3 settle by 3100: key_missing"}) {
		t.Fatalf("narration = %v, want key_missing", recorded)
	}
}

var pastDeadlineSnapshot = chain.PhaseSnapshot{EpochIndex: 410, EffectiveEpochIndex: 410, BlockHeight: 1000}

func lookupAnswering(found func(escrowID string) bool, calls *int) *fakeTxClient {
	return &fakeTxClient{getEscrowFn: func(_ context.Context, escrowID string) (chain.EscrowInfo, bool, error) {
		*calls++
		return chain.EscrowInfo{EscrowID: escrowID, EpochIndex: 400}, found(escrowID), nil
	}}
}

func prunedCheckManager(t *testing.T, testStore *fakeStore, configuration *config.Config, lookup *fakeTxClient, narrator *recordingLifecycleNarrator) *Manager {
	t.Helper()
	deps := testManagerDeps(t, testStore, &fakeTxClient{}, &fakeSnapshotSource{}, configuration)
	deps.ChainFacts = lookup
	if narrator != nil {
		deps.Narrator = narrator
	}
	return mustManager(t, deps)
}

func storeRows(testStore *fakeStore, rows []store.DevshardRecord) {
	for _, row := range rows {
		testStore.devshards[row.EscrowID] = row
	}
}

// Test flow:
//  1. With settlement off, store rows at effective epoch 410: parked and deactivated past the deadline and pruned (the second with only its label), past the deadline but still held by the chain, inside the deadline and not found, serving past the deadline and not found, and one with no label.
//  2. Check what the chain pruned.
//  3. Assert only the two pruned rows out of service past the deadline are marked gone, in the store and in the rows the tick goes on with, each narrated once, and no row is dropped.
func TestARowOutOfServiceThatTheChainPrunedPastItsDeadlineIsMarkedGone(t *testing.T) {
	testStore := newFakeStore()
	rows := []store.DevshardRecord{
		{EscrowID: "parked", Model: "model-a", RotationEpoch: 401, ChainEpoch: 400, Amount: 1000, SettlementPending: true},
		{EscrowID: "deactivated", Model: "model-a", RotationEpoch: 401},
		{EscrowID: "held", Model: "model-a", RotationEpoch: 401, ChainEpoch: 400, Amount: 1000},
		{EscrowID: "fresh", Model: "model-a", RotationEpoch: 410},
		{EscrowID: "serving", Model: "model-a", Active: true, RotationEpoch: 401, ChainEpoch: 400},
		{EscrowID: "unlabelled", Model: "model-a"},
	}
	storeRows(testStore, rows)
	configuration := config.Defaults()
	calls := 0
	narrator := &recordingLifecycleNarrator{}
	manager := prunedCheckManager(t, testStore, &configuration, lookupAnswering(func(escrowID string) bool { return escrowID == "held" }, &calls), narrator)

	checked, err := manager.markPrunedPastDeadline(context.Background(), pastDeadlineSnapshot, rows)

	if err != nil {
		t.Fatalf("markPrunedPastDeadline: %v", err)
	}
	var markedInMemory, markedInStore []string
	for _, record := range checked {
		if record.GoneFromChain {
			markedInMemory = append(markedInMemory, record.EscrowID)
		}
		if testStore.devshards[record.EscrowID].GoneFromChain {
			markedInStore = append(markedInStore, record.EscrowID)
		}
	}
	want := []string{"parked", "deactivated"}
	if len(checked) != len(rows) || !slices.Equal(markedInMemory, want) || !slices.Equal(markedInStore, want) {
		t.Fatalf("rows %d, marked in memory %v, in the store %v, want %d rows and %v marked in both", len(checked), markedInMemory, markedInStore, len(rows), want)
	}
	if got := narrator.recorded(); !slices.Equal(got, []string{"gone from chain deactivated", "gone from chain parked"}) {
		t.Fatalf("narrated %v, want each marked row once", got)
	}
}

// Test flow:
//  1. With settlement on, store a parked row past the deadline whose escrow the chain pruned.
//  2. Check what the chain pruned.
//  3. Assert the chain was not asked and the row is untouched: settlement owns it and drops it itself.
func TestAParkedRowSettlementStillOwnsIsLeftToSettlement(t *testing.T) {
	testStore := newFakeStore()
	row := store.DevshardRecord{EscrowID: "parked", Model: "model-a", RotationEpoch: 401, ChainEpoch: 400, SettlementPending: true}
	storeRows(testStore, []store.DevshardRecord{row})
	configuration := config.Defaults()
	configuration.Rotation.SettlementEnabled = true
	calls := 0
	manager := prunedCheckManager(t, testStore, &configuration, lookupAnswering(func(string) bool { return false }, &calls), nil)

	checked, err := manager.markPrunedPastDeadline(context.Background(), pastDeadlineSnapshot, []store.DevshardRecord{row})

	if err != nil || calls != 0 || checked[0].GoneFromChain || testStore.devshards["parked"].GoneFromChain {
		t.Fatalf("error %v, lookups %d, row %+v, stored %+v, want no lookup and the row untouched", err, calls, checked[0], testStore.devshards["parked"])
	}
}

// Test flow:
//  1. Store a row out of service past the deadline whose settlement was confirmed but whose row was never dropped, and whose escrow the chain has since pruned.
//  2. Check what the chain pruned.
//  3. Assert the row is dropped, from the store and from the rows the tick goes on with, and not recorded as a loss.
func TestASettledRowTheChainPrunedIsDroppedNotRecordedAsALoss(t *testing.T) {
	testStore := newFakeStore()
	row := store.DevshardRecord{EscrowID: "settled", Model: "model-a", RotationEpoch: 401, ChainEpoch: 400, SettleTxHash: "ABCDEF"}
	storeRows(testStore, []store.DevshardRecord{row})
	configuration := config.Defaults()
	calls := 0
	narrator := &recordingLifecycleNarrator{}
	manager := prunedCheckManager(t, testStore, &configuration, lookupAnswering(func(string) bool { return false }, &calls), narrator)

	checked, err := manager.markPrunedPastDeadline(context.Background(), pastDeadlineSnapshot, []store.DevshardRecord{row})

	if _, stored := testStore.devshards["settled"]; err != nil || len(checked) != 0 || stored {
		t.Fatalf("error %v, rows %+v, stored %v, want the row dropped", err, checked, stored)
	}
	if got := narrator.recorded(); slices.Contains(got, "gone from chain settled") {
		t.Fatalf("narrated %v, want no loss recorded for a settled escrow", got)
	}
}

// Test flow:
//  1. Store a pruned row out of service past the deadline, once with a lookup that fails and once with a store that refuses the mark.
//  2. Check what the chain pruned in each.
//  3. Assert neither marks the row, in memory or in the store, and both report the failure.
func TestAFailedLookupOrMarkLeavesTheRowAndIsReported(t *testing.T) {
	row := store.DevshardRecord{EscrowID: "deactivated", Model: "model-a", RotationEpoch: 401, ChainEpoch: 400}
	testCases := []struct {
		name     string
		lookup   *fakeTxClient
		writeErr error
	}{
		{name: "lookup fails", lookup: &fakeTxClient{getEscrowFn: func(context.Context, string) (chain.EscrowInfo, bool, error) {
			return chain.EscrowInfo{}, false, errors.New("chain unreachable")
		}}},
		{name: "mark fails", lookup: lookupAnswering(func(string) bool { return false }, new(int)), writeErr: errors.New("database is locked")},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			testStore := newFakeStore()
			storeRows(testStore, []store.DevshardRecord{row})
			testStore.setActiveErr = testCase.writeErr
			configuration := config.Defaults()
			manager := prunedCheckManager(t, testStore, &configuration, testCase.lookup, nil)

			checked, err := manager.markPrunedPastDeadline(context.Background(), pastDeadlineSnapshot, []store.DevshardRecord{row})

			if err == nil || checked[0].GoneFromChain || testStore.devshards["deactivated"].GoneFromChain {
				t.Fatalf("error %v, row %+v, stored %+v, want the failure reported and the row unmarked", err, checked[0], testStore.devshards["deactivated"])
			}
		})
	}
}

// Test flow:
//  1. Store twenty rows out of service past the deadline that the chain still holds, ordered before four it has pruned.
//  2. Check what the chain pruned on two ticks.
//  3. Assert each tick asks the chain sixteen times, and the second reaches the four pruned rows and marks them: rows the chain still holds do not starve the rest.
func TestRowsTheChainStillHoldsDoNotStarveThePrunedBehindThem(t *testing.T) {
	testStore := newFakeStore()
	rows := make([]store.DevshardRecord, 0, 24)
	for index := range 24 {
		rows = append(rows, store.DevshardRecord{EscrowID: fmt.Sprintf("%02d", index), Model: "model-a", RotationEpoch: 401, ChainEpoch: 400})
	}
	storeRows(testStore, rows)
	configuration := config.Defaults()
	calls := 0
	manager := prunedCheckManager(t, testStore, &configuration, lookupAnswering(func(escrowID string) bool { return escrowID < "20" }, &calls), nil)

	if _, err := manager.markPrunedPastDeadline(context.Background(), pastDeadlineSnapshot, rows); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if calls != prunedChecksPerTick {
		t.Fatalf("lookups on the first tick = %d, want %d", calls, prunedChecksPerTick)
	}
	checked, err := manager.markPrunedPastDeadline(context.Background(), pastDeadlineSnapshot, rows)
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}

	var marked []string
	for _, record := range checked {
		if record.GoneFromChain {
			marked = append(marked, record.EscrowID)
		}
	}
	if calls != 2*prunedChecksPerTick || !slices.Equal(marked, []string{"20", "21", "22", "23"}) {
		t.Fatalf("lookups after two ticks %d, marked %v, want %d and [20 21 22 23]", calls, marked, 2*prunedChecksPerTick)
	}
}

func previousEpochSettlementConfig(modelsJSON string) config.Config {
	configuration := config.Defaults()
	configuration.Rotation.PreviousEpochSettlementEnabled = true
	configuration.Rotation.ModelsJSON = modelsJSON
	return configuration
}

// Test flow:
//  1. With settlement off and previous-epoch settlement on, store parked rows at effective epoch 8 outside any margin: one of chain epoch 8, one of chain epoch 7, one of chain epoch 7 whose model turns previous-epoch settlement off, and one labelled 8 whose chain epoch is unresolved.
//  2. Settle the pending rows once.
//  3. Assert only the chain-epoch-7 row of the model that keeps the flag was settled and dropped; the others stay parked, the unresolved one because its label only bounds its chain epoch from below.
func TestPreviousEpochSettlementSettlesAParkedRowOnlyOnceItsOwnEpochIsOver(t *testing.T) {
	testStore := newFakeStore()
	current := store.DevshardRecord{EscrowID: "current", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", SettlementPending: true, ChainEpoch: 8}
	previous := store.DevshardRecord{EscrowID: "previous", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", SettlementPending: true, ChainEpoch: 7}
	optedOut := store.DevshardRecord{EscrowID: "opted-out", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-b", SettlementPending: true, ChainEpoch: 7}
	unresolved := store.DevshardRecord{EscrowID: "unresolved", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", SettlementPending: true, RotationEpoch: 8}
	rows := []store.DevshardRecord{current, previous, optedOut, unresolved}
	storeRows(testStore, rows)
	configuration := previousEpochSettlementConfig(`[{"model_id":"model-b","amount":1000,"previous_epoch_settlement_enabled":false}]`)
	manager := &Manager{
		tx:               settlingTxClient(),
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
		config:           config.NewHolder(&configuration),
		chainFacts:       &fakeTxClient{},
		now:              time.Now,
	}
	snapshot := chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: 2100, EpochSwitchBlockHeight: 3100}

	if err := manager.settlePending(context.Background(), snapshot, rows); err != nil {
		t.Fatalf("settlePending() = %v, want nil", err)
	}

	if _, ok := testStore.snapshotDevshard(previous.EscrowID); ok {
		t.Error("the chain-epoch-7 row is still stored, want it settled and dropped in epoch 8")
	}
	assertParked(t, testStore, current.EscrowID)
	assertParked(t, testStore, optedOut.EscrowID)
	assertParked(t, testStore, unresolved.EscrowID)
}

// Test flow:
//  1. With settlement off and previous-epoch settlement on, store a serving row of chain epoch 7 inside the margin of the switch into epoch 9.
//  2. Run the deadline pass.
//  3. Assert it was parked and narrated as reached, not as settlement_disabled.
func TestPreviousEpochSettlementParksAServingRowAtItsDeadline(t *testing.T) {
	testStore := newFakeStore()
	row := store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true, ChainEpoch: 7}
	storeRows(testStore, []store.DevshardRecord{row})
	configuration := previousEpochSettlementConfig("")
	deps := testManagerDeps(t, testStore, &fakeTxClient{}, &fakeSnapshotSource{}, &configuration)
	deps.ChainFacts = &fakeTxClient{}
	narrator := &recordingLifecycleNarrator{}
	deps.Narrator = narrator
	manager := mustManager(t, deps)
	snapshot := chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: 2600, EpochSwitchBlockHeight: 3100}

	if _, err := manager.parkAtDeadline(context.Background(), snapshot, []store.DevshardRecord{row}); err != nil {
		t.Fatalf("parkAtDeadline() = %v, want nil", err)
	}

	assertParked(t, testStore, row.EscrowID)
	if recorded := narrator.recorded(); !slices.Contains(recorded, "deadline reached 1 epoch 7 settle by 3100") {
		t.Fatalf("narration = %v, want the deadline reached", recorded)
	}
}

// Test flow:
//  1. With settlement off and previous-epoch settlement on, store a parked row past the deadline whose escrow the chain pruned.
//  2. Check what the chain pruned.
//  3. Assert the chain was not asked and the row is untouched: settlement owns it and drops it itself.
func TestPrunedCheckLeavesAParkedRowToPreviousEpochSettlement(t *testing.T) {
	testStore := newFakeStore()
	row := store.DevshardRecord{EscrowID: "parked", Model: "model-a", RotationEpoch: 401, ChainEpoch: 400, SettlementPending: true}
	storeRows(testStore, []store.DevshardRecord{row})
	configuration := previousEpochSettlementConfig("")
	calls := 0
	manager := prunedCheckManager(t, testStore, &configuration, lookupAnswering(func(string) bool { return false }, &calls), nil)

	checked, err := manager.markPrunedPastDeadline(context.Background(), pastDeadlineSnapshot, []store.DevshardRecord{row})

	if err != nil || calls != 0 || checked[0].GoneFromChain {
		t.Fatalf("error %v, lookups %d, row %+v, want no lookup and the row untouched", err, calls, checked[0])
	}
}

// Test flow:
//  1. Table-driven over readings that only bound the epochs: an unknown effective epoch while the latest is one ahead in PoC, and an unresolved row whose label is the effective epoch, inside the margin.
//  2. With settlement off and previous-epoch settlement on, settle a parked row and run the deadline pass over a serving one.
//  3. Assert neither was settled or parked: an escrow is not settled on a reading that only might be past its own epoch.
func TestPreviousEpochSettlementNeverSettlesOnABoundOfItsOwnEpoch(t *testing.T) {
	testCases := []struct {
		name     string
		epochs   store.DevshardRecord
		snapshot chain.PhaseSnapshot
	}{
		{name: "an unknown effective epoch during PoC", epochs: store.DevshardRecord{ChainEpoch: 8}, snapshot: chain.PhaseSnapshot{EpochIndex: 9, BlockHeight: 2600, EpochSwitchBlockHeight: 3100}},
		{name: "an unresolved row labelled with the effective epoch", epochs: store.DevshardRecord{RotationEpoch: 8}, snapshot: chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: 2600, EpochSwitchBlockHeight: 3100}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			testStore := newFakeStore()
			parked := store.DevshardRecord{EscrowID: "parked", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", SettlementPending: true, ChainEpoch: testCase.epochs.ChainEpoch, RotationEpoch: testCase.epochs.RotationEpoch}
			serving := store.DevshardRecord{EscrowID: "serving", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true, ChainEpoch: testCase.epochs.ChainEpoch, RotationEpoch: testCase.epochs.RotationEpoch}
			rows := []store.DevshardRecord{parked, serving}
			storeRows(testStore, rows)
			configuration := previousEpochSettlementConfig("")
			deps := testManagerDeps(t, testStore, settlingTxClient(), &fakeSnapshotSource{}, &configuration)
			deps.ChainFacts = &fakeTxClient{}
			manager := mustManager(t, deps)

			if _, err := manager.parkAtDeadline(context.Background(), testCase.snapshot, rows); err != nil {
				t.Fatalf("parkAtDeadline() = %v, want nil", err)
			}
			if err := manager.settlePending(context.Background(), testCase.snapshot, rows); err != nil {
				t.Fatalf("settlePending() = %v, want nil", err)
			}

			assertParked(t, testStore, parked.EscrowID)
			if !testStore.devshards[serving.EscrowID].Active {
				t.Fatalf("serving row = %+v, want it still serving", testStore.devshards[serving.EscrowID])
			}
		})
	}
}

// Test flow:
//  1. With both global settlement toggles off, store a parked row of chain epoch 7 for a model that turns previous-epoch settlement on for itself, at effective epoch 8.
//  2. Settle the pending rows once.
//  3. Assert it was settled and dropped.
func TestAModelsOwnPreviousEpochSettlementOverridesTheGlobalToggle(t *testing.T) {
	testStore := newFakeStore()
	row := store.DevshardRecord{EscrowID: "previous", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", SettlementPending: true, ChainEpoch: 7}
	storeRows(testStore, []store.DevshardRecord{row})
	configuration := config.Defaults()
	configuration.Rotation.ModelsJSON = `[{"model_id":"model-a","amount":1000,"previous_epoch_settlement_enabled":true}]`
	deps := testManagerDeps(t, testStore, settlingTxClient(), &fakeSnapshotSource{}, &configuration)
	deps.ChainFacts = &fakeTxClient{}
	manager := mustManager(t, deps)
	snapshot := chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: 2100, EpochSwitchBlockHeight: 3100}

	if err := manager.settlePending(context.Background(), snapshot, []store.DevshardRecord{row}); err != nil {
		t.Fatalf("settlePending() = %v, want nil", err)
	}

	if _, ok := testStore.snapshotDevshard(row.EscrowID); ok {
		t.Fatal("the row is still stored, want the model's own flag to settle it")
	}
}

// Test flow:
//  1. With settlement off and previous-epoch settlement on, store a serving row of chain epoch 7 and a chain client that fails the test if SettleEscrow is called.
//  2. Retire it.
//  3. Assert it was parked, not settled: retirement never settles under previous-epoch settlement alone.
func TestRetireOnlyParksUnderPreviousEpochSettlement(t *testing.T) {
	testStore := newFakeStore()
	row := store.DevshardRecord{EscrowID: "serving", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true, ChainEpoch: 7}
	storeRows(testStore, []store.DevshardRecord{row})
	configuration := previousEpochSettlementConfig("")
	manager := &Manager{
		tx: &fakeTxClient{settleEscrowFn: func(context.Context, *signing.Secp256k1Signer, chain.SettlementInput) (chain.SettleEscrowResult, error) {
			t.Fatal("SettleEscrow must not be called at retirement under previous-epoch settlement alone")
			return chain.SettleEscrowResult{}, nil
		}},
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
		config:           config.NewHolder(&configuration),
	}

	if err := manager.retire(context.Background(), row); err != nil {
		t.Fatalf("retire() = %v, want nil", err)
	}
	assertParked(t, testStore, row.EscrowID)
}
