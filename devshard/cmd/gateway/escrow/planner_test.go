package escrow

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/funding"
	"devshard/cmd/gateway/liquidity"
	"devshard/cmd/gateway/store"
	"devshard/types"
)

// fakeFunds serves fixed money per escrow id.
type fakeFunds map[string]EscrowMoney

func (f fakeFunds) EscrowMoney(escrowID string) (EscrowMoney, bool) {
	money, known := f[escrowID]
	return money, known
}

func planningSnapshot() chain.PhaseSnapshot {
	return chain.PhaseSnapshot{
		EpochIndex: 7, BlockHeight: 1_000, EpochSwitchBlockHeight: 100_000,
		TokenPrice: 1, FeePerNonce: 10, CreateDevshardFee: 100,
		Models: map[string]chain.ModelParams{"qwen": {MaxModelLen: 8192}},
	}
}

// planningManager has one serving qwen escrow holding 40 000, short of a 65 556 slot, and a create that lands.
func planningManager(t *testing.T) (*Manager, *fakeStore, *fakeTxClient, *recordingLifecycleNarrator) {
	t.Helper()
	testStore := newFakeStore()
	testStore.calls = &callLog{}
	testStore.devshards["5"] = store.DevshardRecord{EscrowID: "5", Model: "qwen", Active: true, RotationRole: roleRegular, RotationEpoch: 7}
	txClient := &fakeTxClient{createEscrowFn: succeedingCreateEscrowFn(800)}
	cfg := config.Defaults()
	cfg.Rotation.Enabled = true
	cfg.Rotation.ModelsJSON = `[{"model_id":"qwen","target_count":1,"amount":1000000,"private_key_env":"K"}]`
	narrator := &recordingLifecycleNarrator{}
	deps := testManagerDeps(t, testStore, txClient, &fakeSnapshotSource{snapshot: planningSnapshot()}, &cfg)
	deps.Narrator = narrator
	deps.Funds = fakeFunds{"5": {Config: types.SessionConfig{RefusalTimeout: 60, ExecutionTimeout: 1800}, Balance: 40_000, TokenPrice: 1, FeePerNonce: 10}}
	return mustManager(t, deps), testStore, txClient, narrator
}

// Test flow:
//  1. Build a planning manager with one starved escrow of a model that wants two full escrows and one standby.
//  2. Plan once.
//  3. Assert the narrator heard the escrow starve and a plan of two guard creates, and the report counts the starved escrow, its money, a guarantee two short and the two decisions.
func TestPlanningNarratesAndReportsWhatItDecides(t *testing.T) {
	manager, _, _, narrator := planningManager(t)

	_ = manager.planFunding(context.Background(), true)

	recorded := narrator.recorded()
	for _, want := range []string{"starved 5 free 40000", "planned qwen creates 2 retires 0 reasons guard"} {
		if !slices.Contains(recorded, want) {
			t.Fatalf("narration = %v, want it to contain %q", recorded, want)
		}
	}
	reports := manager.FundingReports()
	if len(reports) != 1 {
		t.Fatalf("FundingReports() = %d reports, want 1", len(reports))
	}
	report := reports[0]
	if report.Guarantee != -2 || report.Counts[CountStarved] != 1 || report.Money[liquidity.ClassFree] != 40_000 {
		t.Fatalf("report = %+v, want guarantee -2, one starved escrow, 40000 free", report)
	}
	if got := report.Decisions[FundingDecision{Action: funding.ActionCreate, Reason: funding.ReasonGuard}]; got != 2 {
		t.Fatalf("guard creates counted = %d, want 2", got)
	}
}

// Test flow:
//  1. Build a planning manager with one starved escrow.
//  2. Plan once.
//  3. Assert the report carries every count state, the ones no escrow is in at zero.
func TestTheFundingReportCountsEveryStateEvenAtZero(t *testing.T) {
	manager, _, _, _ := planningManager(t)

	_ = manager.planFunding(context.Background(), true)

	counts := manager.FundingReports()[0].Counts
	for _, countState := range countStates {
		if _, present := counts[countState]; !present {
			t.Fatalf("report counts = %v, want %q present", counts, countState)
		}
	}
	if counts[CountFull] != 0 || counts[CountStarved] != 1 {
		t.Fatalf("report counts = %v, want full 0 and starved 1", counts)
	}
}

// Test flow:
//  1. Plan once so the model has a report.
//  2. Swap in a configuration whose model list no longer parses and plan again.
//  3. Assert no report is left to export.
func TestAModelListThatNoLongerParsesDropsTheReports(t *testing.T) {
	manager, _, _, _ := planningManager(t)
	_ = manager.planFunding(context.Background(), true)
	broken := *manager.config.Load()
	broken.Rotation.ModelsJSON = "not json"
	manager.config.Swap(&broken)

	_ = manager.planFunding(context.Background(), true)

	if reports := manager.FundingReports(); len(reports) != 0 {
		t.Fatalf("FundingReports() = %v, want none once the model list fails to parse", reports)
	}
}

// Test flow:
//  1. Build a planning manager whose store fails to list rows, and mark its model money-short.
//  2. Plan once.
//  3. Assert the failure was narrated and the mark is still there for the next tick.
func TestAFailedStoreReadKeepsTheMoneyShortMarks(t *testing.T) {
	manager, testStore, _, narrator := planningManager(t)
	testStore.listDevshardsErr = errors.New("store unavailable")
	manager.OnMoneyShort("qwen")

	_ = manager.planFunding(context.Background(), true)

	if recorded := narrator.recorded(); !slices.Contains(recorded, "plan failed: store unavailable") {
		t.Fatalf("narration = %v, want the failed plan", recorded)
	}
	if marks := manager.moneyShort.drain(); !marks["qwen"] {
		t.Fatalf("marks after a failed read = %v, want qwen still marked", marks)
	}
}

// Test flow:
//  1. Build a planning manager with rotation turned off.
//  2. Plan once.
//  3. Assert it read nothing from the store and reports no model.
func TestPlanningWithRotationOffReadsNothing(t *testing.T) {
	manager, testStore, _, _ := planningManager(t)
	rotationOff := *manager.config.Load()
	rotationOff.Rotation.Enabled = false
	manager.config.Swap(&rotationOff)

	_ = manager.planFunding(context.Background(), true)

	if calls := testStore.calls.snapshot(); len(calls) != 0 {
		t.Fatalf("store calls = %v, want none with rotation off", calls)
	}
	if reports := manager.FundingReports(); len(reports) != 0 {
		t.Fatalf("FundingReports() = %v, want none with rotation off", reports)
	}
}

// Test flow:
//  1. Build a planning manager whose model also has a parked row, a settling row and a settled row not yet dropped.
//  2. Read the model.
//  3. Assert the parked and settling rows count as parking, the settled one does not, and all three count against the budget.
func TestReadingAModelCountsParkedAndSettlingRowsAsParking(t *testing.T) {
	manager, testStore, _, _ := planningManager(t)
	testStore.devshards["6"] = store.DevshardRecord{EscrowID: "6", Model: "qwen", SettlementPending: true}
	testStore.devshards["7"] = store.DevshardRecord{EscrowID: "7", Model: "qwen", SettlementPending: true, SettleTxHash: "AB"}
	testStore.devshards["8"] = store.DevshardRecord{EscrowID: "8", Model: "qwen", SettleTxHash: "CD"}
	rows, _ := testStore.ListDevshards(context.Background())
	configuration := manager.config.Load()
	models, _ := rotationModels(configuration.Rotation)

	reading := manager.readModel(models[0], rows, nil, planningSnapshot(), configuration, manager.now())

	if reading.state.Parking != 2 || reading.state.Counted != 4 {
		t.Fatalf("readModel() parking = %d counted = %d, want 2 and 4", reading.state.Parking, reading.state.Counted)
	}
}

// Test flow:
//  1. Read a model with three open create commitments: one regular, one reserve, one temp.
//  2. Assert the regular and the reserve one enter the plan as creates still landing, and the temp one as neither: the bridge sizes its own temps.
func TestReadingAModelCountsOpenCreatesByRole(t *testing.T) {
	manager, _, _, _ := planningManager(t)
	configuration := manager.config.Load()
	models, _ := rotationModels(configuration.Rotation)
	commitments := []store.Commitment{
		{TxHash: "A", Model: "qwen", Role: roleRegular},
		{TxHash: "B", Model: "qwen", Role: RoleReserve},
		{TxHash: "C", Model: "qwen", Role: roleTemp},
	}

	reading := manager.readModel(models[0], nil, commitments, planningSnapshot(), configuration, manager.now())

	if reading.state.PendingRegularCreates != 1 || reading.state.PendingStandbyCreates != 1 {
		t.Fatalf("pending regular %d standby %d, want 1 and 1", reading.state.PendingRegularCreates, reading.state.PendingStandbyCreates)
	}
}

// Test flow:
//  1. Add three active rows no live session answers for: one whose stored amount covers the slot, one with no amount resolved, one below the slot.
//  2. Read and plan the model.
//  3. Assert all three count unread, the two with an amount enter the plan as unread escrows, and only the first counts as full.
func TestReadingAModelCountsAnUnreadRowFullOnlyWhenItsStoredAmountCoversTheSlot(t *testing.T) {
	manager, testStore, _, _ := planningManager(t)
	testStore.devshards["9"] = store.DevshardRecord{EscrowID: "9", Model: "qwen", Active: true, RotationRole: roleRegular, RotationEpoch: 7, Amount: 999_900}
	testStore.devshards["10"] = store.DevshardRecord{EscrowID: "10", Model: "qwen", Active: true, RotationRole: roleRegular, RotationEpoch: 7}
	testStore.devshards["11"] = store.DevshardRecord{EscrowID: "11", Model: "qwen", Active: true, RotationRole: roleRegular, RotationEpoch: 7, Amount: 30_000}
	rows, _ := testStore.ListDevshards(context.Background())
	configuration := manager.config.Load()
	models, _ := rotationModels(configuration.Rotation)

	reading := manager.readModel(models[0], rows, nil, planningSnapshot(), configuration, manager.now())

	var unreadIDs []string
	for _, escrow := range reading.state.Escrows {
		if escrow.Unread {
			unreadIDs = append(unreadIDs, escrow.ID)
		}
	}
	slices.Sort(unreadIDs)
	if fullCount := funding.Plan(reading.state).FullCount; fullCount != 1 || reading.report.Counts[CountUnread] != 3 || !slices.Equal(unreadIDs, []string{"11", "9"}) {
		t.Fatalf("readModel() full count = %d unread = %d unread escrows = %v, want 1, 3 and [11 9]", fullCount, reading.report.Counts[CountUnread], unreadIDs)
	}
}

// planWithRows reads the planning manager's model with the given rows added and the model changed, and plans it.
func planWithRows(t *testing.T, change func(*ModelConfig), rows ...store.DevshardRecord) (modelReading, funding.Decision) {
	t.Helper()
	manager, testStore, _, _ := planningManager(t)
	for _, row := range rows {
		testStore.devshards[row.EscrowID] = row
	}
	stored, _ := testStore.ListDevshards(context.Background())
	configuration := manager.config.Load()
	models, _ := rotationModels(configuration.Rotation)
	change(&models[0])
	reading := manager.readModel(models[0], stored, nil, planningSnapshot(), configuration, manager.now())
	return reading, funding.Plan(reading.state)
}

// Test flow:
//  1. Raise the model's target to two beside its one serving regular of label 7, and add an unread regular of label 7 whose stored amount is known.
//  2. Read and plan the model.
//  3. Assert the spread shortfall is zero: the unread regular serves the label as the escrow it is about to be.
func TestAnUnreadRegularOfTheCurrentLabelStopsASecondSpreadCreate(t *testing.T) {
	_, decision := planWithRows(t, func(model *ModelConfig) { model.TargetCount = 2 },
		store.DevshardRecord{EscrowID: "9", Model: "qwen", Active: true, RotationRole: roleRegular, RotationEpoch: 7, Amount: 999_900})

	if decision.Shortfalls.Spread != 0 {
		t.Fatalf("Plan() spread shortfall = %d, want 0 beside an unread regular of the current label", decision.Shortfalls.Spread)
	}
}

// Test flow:
//  1. Add an unread full regular of 999 900 beside the serving escrow of 40 000.
//  2. Read and plan the model.
//  3. Assert the liquid money adds the unread escrow's stored amount less its full cost to the serving escrow's.
func TestAnUnreadFullEscrowAddsItsStoredAmountToTheLiquidMoney(t *testing.T) {
	reading, decision := planWithRows(t, func(*ModelConfig) {},
		store.DevshardRecord{EscrowID: "9", Model: "qwen", Active: true, RotationRole: roleRegular, RotationEpoch: 7, Amount: 999_900})

	fullCost := reading.state.FullCost
	want := (999_900 - fullCost) + max(40_000, fullCost) - fullCost
	if decision.Liquid != want {
		t.Fatalf("Plan() liquid = %d, want %d: the unread escrow's 999900 less its full cost %d counts", decision.Liquid, want, fullCost)
	}
}

// Test flow:
//  1. Hold the model's budget at its two rows while the guard wants two full escrows, with an unread row of 1 000, the least free starved escrow, beside the serving one of 40 000.
//  2. Read and plan the model.
//  3. Assert budget pressure retires the serving escrow, never the unread one, and the unread row still counts starved in the plan's state.
func TestAnUnreadRowIsNeverChosenForARetire(t *testing.T) {
	reading, decision := planWithRows(t, func(model *ModelConfig) { model.MaxUnsettled = 2 },
		store.DevshardRecord{EscrowID: "12", Model: "qwen", Active: true, RotationRole: roleRegular, RotationEpoch: 7, Amount: 1_000})

	if len(decision.Retires) != 1 || decision.Retires[0].EscrowID != "5" || decision.Retires[0].Reason != funding.ReasonBudgetPressure {
		t.Fatalf("Plan() retires = %+v, want escrow 5 for budget pressure", decision.Retires)
	}
	if !slices.ContainsFunc(reading.state.Escrows, func(escrow funding.EscrowState) bool {
		return escrow.ID == "12" && escrow.Unread && escrow.Money.Starved
	}) {
		t.Fatalf("readModel() escrows = %+v, want 12 unread and starved", reading.state.Escrows)
	}
}

// Test flow:
//  1. Decide once on a wakeup tick and once on a scheduled tick, each with 500 reserved.
//  2. Assert the wakeup recorded no demand sample and was not marked scheduled, and the scheduled one recorded the sample and was marked.
func TestOnlyAScheduledTickSamplesDemand(t *testing.T) {
	planner := &fundingPlanner{models: map[string]*modelFunding{}, escrows: map[string]*escrowFunding{}}
	reading := modelReading{reserved: 500, report: FundingReport{Model: "qwen"}}
	now := time.Unix(1_700_000_000, 0)

	wakeup, _, _ := planner.decide("qwen", reading, false, now)
	scheduled, _, _ := planner.decide("qwen", reading, true, now)

	if wakeup.DemandPeak != 0 || wakeup.ScheduledTick {
		t.Fatalf("decide(wakeup) peak = %d scheduled = %v, want 0 and false", wakeup.DemandPeak, wakeup.ScheduledTick)
	}
	if scheduled.DemandPeak != 500 || !scheduled.ScheduledTick {
		t.Fatalf("decide(scheduled) peak = %d scheduled = %v, want 500 and true", scheduled.DemandPeak, scheduled.ScheduledTick)
	}
}

// Test flow:
//  1. Build a planning manager whose models_json lists the same model twice.
//  2. Plan once.
//  3. Assert the model's decisions were counted once, not once per entry.
func TestAModelListedTwiceIsPlannedOncePerTick(t *testing.T) {
	manager, _, _, _ := planningManager(t)
	configuration := manager.config.Load()
	duplicated := *configuration
	duplicated.Rotation.ModelsJSON = `[{"model_id":"qwen","target_count":1,"amount":1000000,"private_key_env":"K","full_context_slots":1,"reserve_count":0},{"model_id":"qwen","target_count":1,"amount":1000000,"private_key_env":"K","full_context_slots":1,"reserve_count":0}]`
	manager.config.Swap(&duplicated)

	_ = manager.planFunding(context.Background(), true)

	reports := manager.FundingReports()
	if len(reports) != 1 {
		t.Fatalf("FundingReports() = %d reports, want 1", len(reports))
	}
	if got := reports[0].Decisions[FundingDecision{Action: funding.ActionCreate, Reason: funding.ReasonGuard}]; got != 1 {
		t.Fatalf("planFunding(duplicate model) counted %d guard creates, want 1", got)
	}
}

// Test flow:
//  1. Build a manager from the default configuration with rotation on and nothing else changed: one serving qwen escrow holding 40 000, short of a 65 556 slot, its money readable, and a create that lands.
//  2. Run one tick.
//  3. Assert the tick planned and executed the guarantee's two guard creates.
func TestTheDefaultConfigurationRunsThePlanner(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["5"] = store.DevshardRecord{EscrowID: "5", Model: "qwen", Active: true, RotationRole: roleRegular, RotationEpoch: 7, PrivateKeyEnv: "K"}
	txClient := &fakeTxClient{createEscrowFn: succeedingCreateEscrowFn(800)}
	cfg := config.Defaults()
	cfg.Rotation.Enabled = true
	cfg.Rotation.ModelsJSON = `[{"model_id":"qwen","target_count":1,"reserve_count":0,"amount":1000000,"private_key_env":"K"}]`
	deps := testManagerDeps(t, testStore, txClient, &fakeSnapshotSource{snapshot: planningSnapshot()}, &cfg)
	deps.Funds = fakeFunds{"5": {Config: types.SessionConfig{RefusalTimeout: 60, ExecutionTimeout: 1800}, Balance: 40_000, TokenPrice: 1, FeePerNonce: 10}}
	manager := mustManager(t, deps)

	if err := manager.tick(context.Background()); err != nil {
		t.Fatalf("tick() = %v, want nil", err)
	}

	if txClient.createCalls != 2 {
		t.Fatalf("CreateEscrow calls = %d, want the guarantee's 2 guard creates", txClient.createCalls)
	}
}
