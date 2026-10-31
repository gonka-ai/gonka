package escrow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/store"
	"devshard/signing"
)

// succeedingCreateEscrowFn always succeeds, assigning sequential escrow ids/tx hashes from startID.
func succeedingCreateEscrowFn(startID uint64) func(context.Context, *signing.Secp256k1Signer, uint64, string, func(string) error) (chain.CreateEscrowResult, error) {
	nextID := startID
	return func(ctx context.Context, signer *signing.Secp256k1Signer, amount uint64, modelID string, onPrepared func(string) error) (chain.CreateEscrowResult, error) {
		id := nextID
		nextID++
		txHash := fmt.Sprintf("TX-%d", id)
		if err := onPrepared(txHash); err != nil {
			return chain.CreateEscrowResult{}, err
		}
		return chain.CreateEscrowResult{EscrowID: id, TxHash: txHash}, nil
	}
}

func servedSnapshot(epoch uint64, blockHeight int64, modelID string) chain.PhaseSnapshot {
	return chain.PhaseSnapshot{
		EpochIndex:         epoch,
		BlockHeight:        blockHeight,
		FullWeightsByModel: map[string]map[string]float64{modelID: {"participant": 1}},
	}
}

func newRotationManager(t *testing.T, testStore *fakeStore, txClient *fakeTxClient, settlementEnabled bool) *Manager {
	t.Helper()
	return &Manager{
		tx:               txClient,
		store:            testStore,
		signer:           &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{},
		breaker:          newCreateBreaker(),
		now:              func() time.Time { return time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC) },
		config:           holderWithSettlementEnabled(settlementEnabled),
	}
}

func failOnCreate(t *testing.T) func(context.Context, *signing.Secp256k1Signer, uint64, string, func(string) error) (chain.CreateEscrowResult, error) {
	return func(context.Context, *signing.Secp256k1Signer, uint64, string, func(string) error) (chain.CreateEscrowResult, error) {
		t.Fatal("createEscrow must not be called")
		return chain.CreateEscrowResult{}, nil
	}
}

// Test flow:
//  1. Seed the store with one existing active temp devshard record for model-a.
//  2. Call `fillToLabel` for temps with a target of 3 against that one existing record.
//  3. Assert it returns nil with created=2, the shortfall between the target and what already exists.
//  4. Assert the chain client's CreateEscrow was called exactly twice.
func TestTheBridgeFillCreatesExactlyTheShortfall(t *testing.T) {
	testStore := newFakeStore()
	existing := store.DevshardRecord{EscrowID: "existing-1", Model: "model-a", Active: true, RotationRole: roleTemp, RotationEpoch: 5}
	testStore.devshards[existing.EscrowID] = existing
	txClient := &fakeTxClient{createEscrowFn: succeedingCreateEscrowFn(100)}
	m := newRotationManager(t, testStore, txClient, false)
	model := ModelConfig{ModelID: "model-a", Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}
	snapshot := servedSnapshot(5, 100, "model-a")

	fill := m.fillToLabel(context.Background(), roleTemp, snapshot.EpochIndex, []ModelConfig{model}, func(ModelConfig) int { return 3 }, snapshot, []store.DevshardRecord{existing})[0]
	created, err := fill.created, fill.err
	if err != nil {
		t.Fatalf("fillToLabel(): %v", err)
	}
	if created != 2 {
		t.Fatalf("created = %d, want 2 (target 3 minus 1 existing)", created)
	}
	if txClient.createCalls != 2 {
		t.Fatalf("createCalls = %d, want 2", txClient.createCalls)
	}
}

// Test flow:
//  1. Seed a devshard record for each of the case's existing count, and build a chain client that fails the test if CreateEscrow is ever called.
//  2. Call `fillToLabel` for temps with the case's target.
//  3. Assert it returns nil with created=0, since nothing new is needed. The table varies existing count vs target: exactly at target, and over target.
func TestTheBridgeFillAtOrOverTargetCreatesNothing(t *testing.T) {
	tests := []struct {
		name     string
		existing int
		target   int
	}{
		{name: "exactly at target", existing: 2, target: 2},
		{name: "over target", existing: 3, target: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			testStore := newFakeStore()
			devshards := make([]store.DevshardRecord, 0, tt.existing)
			for i := range tt.existing {
				record := store.DevshardRecord{EscrowID: fmt.Sprintf("existing-%d", i), Model: "model-a", Active: true, RotationRole: roleTemp, RotationEpoch: 5}
				devshards = append(devshards, record)
			}
			m := newRotationManager(t, testStore, &fakeTxClient{createEscrowFn: failOnCreate(t)}, false)
			model := ModelConfig{ModelID: "model-a", Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}
			snapshot := servedSnapshot(5, 100, "model-a")

			fill := m.fillToLabel(context.Background(), roleTemp, snapshot.EpochIndex, []ModelConfig{model}, func(ModelConfig) int { return tt.target }, snapshot, devshards)[0]
			created, err := fill.created, fill.err
			if err != nil {
				t.Fatalf("fillToLabel(): %v", err)
			}
			if created != 0 {
				t.Fatalf("created = %d, want 0", created)
			}
		})
	}
}

// Test flow:
//  1. Record a create-breaker failure for model-a/temp so its cooldown is armed.
//  2. Build a snapshot with no weights, so the model is never treated as served or unserved (a cold start).
//  3. Call `fillToLabel` for temps already at target with one existing record.
//  4. Assert it returns nil with created=0.
//  5. Assert the breaker is still gated, since an at-target call must short-circuit before reaching the breaker and must not consume its cooldown.
func TestTheBridgeFillAtTargetDoesNotConsumeBreakerCooldown(t *testing.T) {
	testStore := newFakeStore()
	m := newRotationManager(t, testStore, &fakeTxClient{createEscrowFn: failOnCreate(t)}, false)
	m.breaker.recordFailure("model-a", roleTemp)
	model := ModelConfig{ModelID: "model-a", Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}
	snapshot := chain.PhaseSnapshot{EpochIndex: 5, BlockHeight: 100}
	existing := []store.DevshardRecord{{EscrowID: "existing-1", Model: "model-a", Active: true, RotationRole: roleTemp, RotationEpoch: 5}}

	fill := m.fillToLabel(context.Background(), roleTemp, snapshot.EpochIndex, []ModelConfig{model}, func(ModelConfig) int { return 1 }, snapshot, existing)[0]
	created, err := fill.created, fill.err
	if err != nil {
		t.Fatalf("fillToLabel(): %v", err)
	}
	if created != 0 {
		t.Fatalf("created = %d, want 0", created)
	}
	if !m.breaker.gated("model-a", roleTemp) {
		t.Fatal("breaker cooldown was consumed by an at-target call that should have short-circuited before reaching the breaker")
	}
}

// Test flow:
//  1. Build a snapshot where the network serves only model-b, so model-a is known but not served.
//  2. Call `fillToLabel` for model-a's temps against that snapshot.
//  3. Assert it returns nil with created=0, since an unserved model is skipped.
func TestTheBridgeFillSkipsAModelTheNetworkDoesNotServe(t *testing.T) {
	testStore := newFakeStore()
	m := newRotationManager(t, testStore, &fakeTxClient{createEscrowFn: failOnCreate(t)}, false)
	model := ModelConfig{ModelID: "model-a", Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}
	snapshot := servedSnapshot(5, 100, "model-b")

	fill := m.fillToLabel(context.Background(), roleTemp, snapshot.EpochIndex, []ModelConfig{model}, func(ModelConfig) int { return 3 }, snapshot, nil)[0]
	created, err := fill.created, fill.err
	if err != nil {
		t.Fatalf("fillToLabel(): %v", err)
	}
	if created != 0 {
		t.Fatalf("created = %d, want 0", created)
	}
}

// Test flow:
//  1. Record a create-breaker failure for model-a/temp so it is gated.
//  2. Call `fillToLabel` for temps against a served snapshot.
//  3. Assert it returns `errCreateSuppressed` with created=0.
func TestTheBridgeFillReportsSuppressionWhenTheBreakerIsGated(t *testing.T) {
	testStore := newFakeStore()
	m := newRotationManager(t, testStore, &fakeTxClient{createEscrowFn: failOnCreate(t)}, false)
	m.breaker.recordFailure("model-a", roleTemp)
	model := ModelConfig{ModelID: "model-a", Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}
	snapshot := servedSnapshot(5, 100, "model-a")

	fill := m.fillToLabel(context.Background(), roleTemp, snapshot.EpochIndex, []ModelConfig{model}, func(ModelConfig) int { return 3 }, snapshot, nil)[0]
	created, err := fill.created, fill.err
	if !errors.Is(err, errCreateSuppressed) {
		t.Fatalf("fillToLabel() = %v, want errCreateSuppressed: a gated create is not a success the bridge may retire against", err)
	}
	if created != 0 {
		t.Fatalf("created = %d, want 0", created)
	}
}

// Test flow:
//  1. Build a chain client whose CreateEscrow broadcasts then fails.
//  2. Call `fillToLabel` for temps and assert the broadcast failure is surfaced, created=0, and CreateEscrow was called exactly once.
//  3. Call `fillToLabel` again and assert it now returns `errCreateSuppressed` from the gated breaker, without any further CreateEscrow call.
func TestTheBridgeFillStopsOnTheFirstErrorAndGatesTheBreaker(t *testing.T) {
	testStore := newFakeStore()
	broadcastErr := errors.New("broadcast rejected")
	txClient := &fakeTxClient{createEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, amount uint64, modelID string, onPrepared func(string) error) (chain.CreateEscrowResult, error) {
		if err := onPrepared("TX-FAIL"); err != nil {
			return chain.CreateEscrowResult{}, err
		}
		return chain.CreateEscrowResult{}, broadcastErr
	}}
	m := newRotationManager(t, testStore, txClient, false)
	model := ModelConfig{ModelID: "model-a", Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}
	snapshot := servedSnapshot(5, 100, "model-a")

	fill := m.fillToLabel(context.Background(), roleTemp, snapshot.EpochIndex, []ModelConfig{model}, func(ModelConfig) int { return 3 }, snapshot, nil)[0]
	created, err := fill.created, fill.err
	if err == nil {
		t.Fatal("fillToLabel() error = nil, want the broadcast failure surfaced")
	}
	if created != 0 {
		t.Fatalf("created = %d, want 0", created)
	}
	if txClient.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1 (stop at the first error)", txClient.createCalls)
	}

	secondFill := m.fillToLabel(context.Background(), roleTemp, snapshot.EpochIndex, []ModelConfig{model}, func(ModelConfig) int { return 3 }, snapshot, nil)[0]
	created2, err2 := secondFill.created, secondFill.err
	if !errors.Is(err2, errCreateSuppressed) {
		t.Fatalf("second fillToLabel() = %v, want errCreateSuppressed (the now-gated breaker)", err2)
	}
	if created2 != 0 {
		t.Fatalf("created on the gated call = %d, want 0", created2)
	}
	if txClient.createCalls != 1 {
		t.Fatalf("createCalls after the gated call = %d, want still 1", txClient.createCalls)
	}
}

// Test flow:
//  1. Seed the store with two active regular devshard records for model-a (one with an empty rotation role).
//  2. Call `prepareBridge` with a TempCount of 1 against a snapshot that serves model-a.
//  3. Assert it returns nil.
//  4. Assert both regulars end up parked and exactly one temp escrow is created for model-a.
//  5. Assert the saved rotation status is Completed with Stage stagePrepareTemp.
func TestPrepareBridgeTempReachesTargetRetiresRegulars(t *testing.T) {
	testStore := newFakeStore()
	regularOne := store.DevshardRecord{EscrowID: "reg-1", Model: "model-a", Active: true, RotationRole: roleRegular, PrivateKeyEnv: "MODEL_A_KEY"}
	regularTwo := store.DevshardRecord{EscrowID: "reg-2", Model: "model-a", Active: true, RotationRole: "", PrivateKeyEnv: "MODEL_A_KEY"}
	testStore.devshards[regularOne.EscrowID] = regularOne
	testStore.devshards[regularTwo.EscrowID] = regularTwo
	txClient := &fakeTxClient{createEscrowFn: succeedingCreateEscrowFn(200)}
	m := newRotationManager(t, testStore, txClient, false)
	models := []ModelConfig{{ModelID: "model-a", TempCount: 1, TargetCount: 3, Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}}
	snapshot := servedSnapshot(9, 500, "model-a")
	devshards := []store.DevshardRecord{regularOne, regularTwo}

	if err := m.prepareBridge(context.Background(), snapshot, models, devshards); err != nil {
		t.Fatalf("prepareBridge(): %v", err)
	}

	assertParked(t, testStore, "reg-1")
	assertParked(t, testStore, "reg-2")
	tempCount := 0
	for _, record := range testStore.devshards {
		if record.Model == "model-a" && record.RotationRole == roleTemp {
			tempCount++
		}
	}
	if tempCount != 1 {
		t.Fatalf("temp escrows for model-a = %d, want 1 (TempCount target)", tempCount)
	}
	status, ok := testStore.rotationStatuses["model-a|"+roleTemp]
	if !ok {
		t.Fatal("no rotation status saved for model-a/temp")
	}
	if !status.Completed || status.Stage != stagePrepareTemp {
		t.Fatalf("rotation status = %+v, want Completed=true Stage=%q", status, stagePrepareTemp)
	}
}

// Test flow:
//  1. Seed the store with one active regular devshard record for model-a.
//  2. Build a chain client whose CreateEscrow broadcasts then fails.
//  3. Call `prepareBridge` and assert the create failure is surfaced.
//  4. Assert the regular is kept active but promoted to the temp role instead of being retired.
//  5. Assert the saved rotation status is not Completed and carries a non-empty CreateError.
func TestPrepareBridgeTempCreateFailsPromotesRegularsInstead(t *testing.T) {
	testStore := newFakeStore()
	regular := store.DevshardRecord{EscrowID: "reg-1", Model: "model-a", Active: true, RotationRole: roleRegular, PrivateKeyEnv: "MODEL_A_KEY"}
	testStore.devshards[regular.EscrowID] = regular
	broadcastErr := errors.New("broadcast rejected")
	txClient := &fakeTxClient{createEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, amount uint64, modelID string, onPrepared func(string) error) (chain.CreateEscrowResult, error) {
		if err := onPrepared("TX-FAIL"); err != nil {
			return chain.CreateEscrowResult{}, err
		}
		return chain.CreateEscrowResult{}, broadcastErr
	}}
	m := newRotationManager(t, testStore, txClient, false)
	models := []ModelConfig{{ModelID: "model-a", TempCount: 1, TargetCount: 3, Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}}
	snapshot := servedSnapshot(9, 500, "model-a")
	devshards := []store.DevshardRecord{regular}

	if err := m.prepareBridge(context.Background(), snapshot, models, devshards); err == nil {
		t.Fatal("prepareBridge() error = nil, want the create failure surfaced")
	}

	got, ok := testStore.devshards["reg-1"]
	if !ok {
		t.Fatal("reg-1 missing after a failed prepareBridge, want it kept (promoted, not retired)")
	}
	if !got.Active {
		t.Fatal("reg-1.Active = false, want true (promote, not retire)")
	}
	if got.RotationRole != roleTemp {
		t.Fatalf("reg-1.RotationRole = %q, want %q (promoted)", got.RotationRole, roleTemp)
	}
	status, ok := testStore.rotationStatuses["model-a|"+roleTemp]
	if !ok {
		t.Fatal("no rotation status saved for model-a/temp")
	}
	if status.Completed {
		t.Fatal("rotation status Completed = true, want false")
	}
	if status.CreateError == "" {
		t.Fatal("rotation status CreateError is empty, want the create failure message")
	}
}

// Test flow:
//  1. Seed the store with one active regular devshard record for model-a and gate the create breaker for model-a/temp.
//  2. Build a chain client that fails the test if CreateEscrow is ever called.
//  3. Call `prepareBridge`.
//  4. Assert the regular is kept active and promoted to the temp role, since no temp was created to take over from it.
//  5. Assert the returned error is `errCreateSuppressed`.
func TestPrepareBridgeGatedBreakerPromotesRegularsInsteadOfRetiringThem(t *testing.T) {
	testStore := newFakeStore()
	regular := store.DevshardRecord{EscrowID: "reg-1", Model: "model-a", Active: true, RotationRole: roleRegular, PrivateKeyEnv: "MODEL_A_KEY"}
	testStore.devshards[regular.EscrowID] = regular
	m := newRotationManager(t, testStore, &fakeTxClient{createEscrowFn: failOnCreate(t)}, false)
	m.breaker.recordFailure("model-a", roleTemp)
	models := []ModelConfig{{ModelID: "model-a", TempCount: 1, TargetCount: 1, Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}}
	snapshot := servedSnapshot(9, 500, "model-a")
	devshards := []store.DevshardRecord{regular}

	err := m.prepareBridge(context.Background(), snapshot, models, devshards)

	got, ok := testStore.devshards["reg-1"]
	if !ok || !got.Active {
		t.Fatalf("reg-1 = %+v ok=%v, want it kept serving: no temp was created to take over from it", got, ok)
	}
	if got.RotationRole != roleTemp {
		t.Fatalf("reg-1.RotationRole = %q, want %q (promoted by the degrade path)", got.RotationRole, roleTemp)
	}
	if !errors.Is(err, errCreateSuppressed) {
		t.Fatalf("prepareBridge() = %v, want the suppressed create surfaced", err)
	}
}

// Test flow:
//  1. Build a chain client whose CreateEscrow fails for model-a but succeeds for model-b.
//  2. Call `prepareBridge` for both models against a snapshot that serves both.
//  3. Assert the returned error surfaces model-a's failure.
//  4. Assert model-b's rotation status is still saved and Completed, while model-a's is not.
func TestPrepareBridgeOneModelFailureDoesNotStopOthers(t *testing.T) {
	testStore := newFakeStore()
	broadcastErr := errors.New("broadcast rejected")
	txClient := &fakeTxClient{createEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, amount uint64, modelID string, onPrepared func(string) error) (chain.CreateEscrowResult, error) {
		if modelID == "model-a" {
			if err := onPrepared("TX-A-FAIL"); err != nil {
				return chain.CreateEscrowResult{}, err
			}
			return chain.CreateEscrowResult{}, broadcastErr
		}
		if err := onPrepared("TX-B-OK"); err != nil {
			return chain.CreateEscrowResult{}, err
		}
		return chain.CreateEscrowResult{EscrowID: 900, TxHash: "TX-B-OK"}, nil
	}}
	m := newRotationManager(t, testStore, txClient, false)
	models := []ModelConfig{
		{ModelID: "model-a", TempCount: 1, TargetCount: 1, Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"},
		{ModelID: "model-b", TempCount: 1, TargetCount: 1, Amount: 1000, PrivateKeyEnv: "MODEL_B_KEY"},
	}
	snapshot := chain.PhaseSnapshot{EpochIndex: 9, BlockHeight: 500, FullWeightsByModel: map[string]map[string]float64{
		"model-a": {"p": 1}, "model-b": {"p": 1},
	}}

	if err := m.prepareBridge(context.Background(), snapshot, models, nil); err == nil {
		t.Fatal("prepareBridge() error = nil, want model-a's failure surfaced")
	}

	statusB, ok := testStore.rotationStatuses["model-b|"+roleTemp]
	if !ok {
		t.Fatal("no rotation status saved for model-b, want it processed despite model-a's failure")
	}
	if !statusB.Completed {
		t.Fatalf("model-b rotation status = %+v, want Completed=true", statusB)
	}
	if statusA := testStore.rotationStatuses["model-a|"+roleTemp]; statusA.Completed {
		t.Fatal("model-a rotation status Completed = true, want false")
	}
}

// Test flow:
//  1. Build a chain client whose wallet pays for two creates and refuses every later one as underfunded.
//  2. Call `prepareBridge` for two models that each want two temps.
//  3. Assert each model got one temp: a pass funds one create per model before any model gets a second.
func TestPrepareBridgeSharesAShortWalletAcrossModels(t *testing.T) {
	testStore := newFakeStore()
	paidCreates := 0
	createdByModel := map[string]int{}
	fundedCreate := succeedingCreateEscrowFn(900)
	txClient := &fakeTxClient{createEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, amount uint64, modelID string, onPrepared func(string) error) (chain.CreateEscrowResult, error) {
		if paidCreates == 2 {
			return chain.CreateEscrowResult{}, &chain.WalletUnderfundedError{Address: "gonka1wallet", Have: 0, Need: amount}
		}
		paidCreates++
		createdByModel[modelID]++
		return fundedCreate(ctx, signer, amount, modelID, onPrepared)
	}}
	m := newRotationManager(t, testStore, txClient, false)
	models := []ModelConfig{
		{ModelID: "model-a", TempCount: 2, TargetCount: 2, Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"},
		{ModelID: "model-b", TempCount: 2, TargetCount: 2, Amount: 1000, PrivateKeyEnv: "MODEL_B_KEY"},
	}
	snapshot := chain.PhaseSnapshot{EpochIndex: 9, BlockHeight: 500, FullWeightsByModel: map[string]map[string]float64{
		"model-a": {"p": 1}, "model-b": {"p": 1},
	}}

	_ = m.prepareBridge(context.Background(), snapshot, models, nil)

	if createdByModel["model-a"] != 1 || createdByModel["model-b"] != 1 {
		t.Fatalf("creates by model = %v, want one each: a short wallet must not fund one model twice while another gets none", createdByModel)
	}
}

// Test flow:
//  1. Configure the fake store's SaveRotationStatus to fail.
//  2. Call `prepareBridge` with a chain client that otherwise succeeds.
//  3. Assert the returned error surfaces the rotation-status save failure even though the rotation itself succeeded.
func TestPrepareBridgeSurfacesSaveRotationStatusFailure(t *testing.T) {
	testStore := newFakeStore()
	testStore.saveRotationStatusErr = errors.New("status store unavailable")
	txClient := &fakeTxClient{createEscrowFn: succeedingCreateEscrowFn(400)}
	m := newRotationManager(t, testStore, txClient, false)
	models := []ModelConfig{{ModelID: "model-a", TempCount: 1, TargetCount: 1, Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}}
	snapshot := servedSnapshot(9, 500, "model-a")

	if err := m.prepareBridge(context.Background(), snapshot, models, nil); err == nil {
		t.Fatal("prepareBridge() error = nil, want the rotation-status save failure surfaced even though rotation itself succeeded")
	}
}

// Test flow:
//  1. Seed the store with one active regular devshard record for model-a and a shared call log across the store and chain client.
//  2. Build a Manager with the settlement toggle enabled and a chain client that both creates and settles successfully.
//  3. Call `prepareBridge`.
//  4. Assert it returns nil, the call log recorded a SettleEscrow broadcast, and the regular's row is gone after the settle-then-retire.
func TestPrepareBridgeSettlementEnabledSettlesRegularsBeforeRetiring(t *testing.T) {
	testStore := newFakeStore()
	regular := store.DevshardRecord{EscrowID: "reg-1", Model: "model-a", Active: true, RotationRole: roleRegular, PrivateKeyEnv: "MODEL_A_KEY"}
	testStore.devshards[regular.EscrowID] = regular
	log := &callLog{}
	testStore.calls = log
	txClient := &fakeTxClient{
		createEscrowFn: succeedingCreateEscrowFn(500),
		calls:          log,
		settleEscrowFn: func(ctx context.Context, signer *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
			return chain.SettleEscrowResult{EscrowID: input.EscrowID}, nil
		},
	}
	m := &Manager{
		tx: txClient, store: testStore, signer: &fakeSignerSource{signer: testSigner(t)},
		breaker: newCreateBreaker(), now: time.Now,
		config:           holderWithSettlementEnabled(true),
		settlementSource: &fakeSettlementSource{busy: false, calls: log},
	}
	models := []ModelConfig{{ModelID: "model-a", TempCount: 1, TargetCount: 1, Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}}
	snapshot := servedSnapshot(9, 500, "model-a")
	devshards := []store.DevshardRecord{regular}

	if err := m.prepareBridge(context.Background(), snapshot, models, devshards); err != nil {
		t.Fatalf("prepareBridge(): %v", err)
	}

	settled := false
	for _, name := range log.snapshot() {
		if name == "SettleEscrow" {
			settled = true
		}
	}
	if !settled {
		t.Fatal("SettleEscrow never called, want retire() to settle the regular escrow on chain when settlement is enabled")
	}
	if _, ok := testStore.devshards["reg-1"]; ok {
		t.Fatal("reg-1 still present after a successful settle+retire")
	}
}

// Test flow:
//  1. Seed the store with five devshard records for model-a and model-b spanning active regular, active with an empty role, already-temp, another model's regular, and an inactive regular.
//  2. Call `promoteRegularsToTemp` for model-a.
//  3. Assert it returns nil with promoted=2.
//  4. Assert only the active model-a regulars are relabeled to the temp role; the other model's record and the inactive record stay unchanged.
func TestPromoteRegularsToTempRelabelsActiveRegularsOnly(t *testing.T) {
	testStore := newFakeStore()
	regularOne := store.DevshardRecord{EscrowID: "reg-1", Model: "model-a", Active: true, RotationRole: roleRegular}
	regularEmpty := store.DevshardRecord{EscrowID: "reg-2", Model: "model-a", Active: true, RotationRole: ""}
	alreadyTemp := store.DevshardRecord{EscrowID: "temp-1", Model: "model-a", Active: true, RotationRole: roleTemp}
	otherModel := store.DevshardRecord{EscrowID: "reg-3", Model: "model-b", Active: true, RotationRole: roleRegular}
	inactive := store.DevshardRecord{EscrowID: "reg-4", Model: "model-a", Active: false, RotationRole: roleRegular}
	for _, record := range []store.DevshardRecord{regularOne, regularEmpty, alreadyTemp, otherModel, inactive} {
		testStore.devshards[record.EscrowID] = record
	}
	m := newRotationManager(t, testStore, &fakeTxClient{}, false)
	model := ModelConfig{ModelID: "model-a"}
	devshards := []store.DevshardRecord{regularOne, regularEmpty, alreadyTemp, otherModel, inactive}

	promoted, err := m.promoteRegularsToTemp(context.Background(), model, devshards)
	if err != nil {
		t.Fatalf("promoteRegularsToTemp(): %v", err)
	}
	if promoted != 2 {
		t.Fatalf("promoted = %d, want 2", promoted)
	}
	if got := testStore.devshards["reg-1"].RotationRole; got != roleTemp {
		t.Fatalf("reg-1.RotationRole = %q, want %q", got, roleTemp)
	}
	if got := testStore.devshards["reg-2"].RotationRole; got != roleTemp {
		t.Fatalf("reg-2.RotationRole = %q, want %q", got, roleTemp)
	}
	if got := testStore.devshards["reg-3"].RotationRole; got != roleRegular {
		t.Fatalf("reg-3 (other model) RotationRole = %q, want unchanged %q", got, roleRegular)
	}
	if got := testStore.devshards["reg-4"].RotationRole; got != roleRegular {
		t.Fatalf("reg-4 (inactive) RotationRole = %q, want unchanged %q", got, roleRegular)
	}
}

// Test flow:
//  1. Seed the store with two active regular devshard records for model-a and configure the fake store to fail the rotation-role write for reg-1.
//  2. Call `promoteRegularsToTemp`.
//  3. Assert the returned error wraps reg-1's write failure, with promoted=1.
//  4. Assert reg-2 is promoted to the temp role despite reg-1's failure, while reg-1 stays unchanged.
func TestPromoteRegularsToTempContinuesPastErrorReturnsFirst(t *testing.T) {
	testStore := newFakeStore()
	regularOne := store.DevshardRecord{EscrowID: "reg-1", Model: "model-a", Active: true, RotationRole: roleRegular}
	regularTwo := store.DevshardRecord{EscrowID: "reg-2", Model: "model-a", Active: true, RotationRole: roleRegular}
	testStore.devshards[regularOne.EscrowID] = regularOne
	testStore.devshards[regularTwo.EscrowID] = regularTwo
	writeFailure := errors.New("store unavailable")
	testStore.rotationRoleErrByID = map[string]error{"reg-1": writeFailure}
	m := newRotationManager(t, testStore, &fakeTxClient{}, false)
	model := ModelConfig{ModelID: "model-a"}
	devshards := []store.DevshardRecord{regularOne, regularTwo}

	promoted, err := m.promoteRegularsToTemp(context.Background(), model, devshards)
	if err == nil {
		t.Fatal("promoteRegularsToTemp() error = nil, want reg-1's write failure surfaced")
	}
	if !errors.Is(err, writeFailure) {
		t.Fatalf("promoteRegularsToTemp() error = %v, want it to wrap %v", err, writeFailure)
	}
	if promoted != 1 {
		t.Fatalf("promoted = %d, want 1 (reg-2 still promoted despite reg-1's failure)", promoted)
	}
	if got := testStore.devshards["reg-2"].RotationRole; got != roleTemp {
		t.Fatalf("reg-2.RotationRole = %q, want %q (continued past reg-1's failure)", got, roleTemp)
	}
	if got := testStore.devshards["reg-1"].RotationRole; got != roleRegular {
		t.Fatalf("reg-1.RotationRole = %q, want unchanged %q (write failed)", got, roleRegular)
	}
}

// Test flow:
//  1. Call `deferredRetire` with each case's error.
//  2. Assert the result matches the case's expected deferred flag. The table varies the error: a draining escrow (ErrDevshardBusy), a settlement already in flight (ErrSettlementInFlight), the same busy error wrapped, a missing signing key, and an unknown escrow — the first three are retried later, the last two are terminal.
func TestDeferredRetireSeparatesNotYetFromFailed(t *testing.T) {
	testCases := []struct {
		name     string
		err      error
		deferred bool
	}{
		{name: "a draining escrow is retried", err: ErrDevshardBusy, deferred: true},
		{name: "a settlement already running is retried", err: ErrSettlementInFlight, deferred: true},
		{name: "wrapped, still retried", err: fmt.Errorf("retiring escrow 7: %w", ErrDevshardBusy), deferred: true},
		{name: "a missing signing key is not", err: errors.New("resolving signer for escrow 7"), deferred: false},
		{name: "an unknown escrow is not", err: ErrUnknownEscrow, deferred: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := deferredRetire(testCase.err); got != testCase.deferred {
				t.Fatalf("deferredRetire(%v) = %v, want %v", testCase.err, got, testCase.deferred)
			}
		})
	}
}

// Test flow:
//  1. Seed the store with one active regular devshard record for model-a, then build a stale slice holding a copy of it from before the current tick's other writes.
//  2. Deactivate the row and record a settle tx hash directly on the store, simulating an earlier step in the same tick.
//  3. Call `promoteRegularsToTemp` with the stale slice.
//  4. Assert it returns nil, the row is promoted to the temp role, stays inactive rather than being resurrected, and keeps the settle tx hash the tick already recorded.
func TestPromotingToTempKeepsWhatTheSameTickAlreadyWrote(t *testing.T) {
	testStore := newFakeStore()
	stored := store.DevshardRecord{EscrowID: "reg-1", Model: "model-a", Active: true, RotationRole: roleRegular}
	testStore.devshards[stored.EscrowID] = stored
	m := newRotationManager(t, testStore, &fakeTxClient{}, false)
	stale := []store.DevshardRecord{stored}
	if err := testStore.SetDevshardActive(context.Background(), "reg-1", false); err != nil {
		t.Fatalf("SetDevshardActive(): %v", err)
	}
	if err := testStore.SetDevshardSettleTxHash(context.Background(), "reg-1", "SETTLE-TX"); err != nil {
		t.Fatalf("SetDevshardSettleTxHash(): %v", err)
	}

	if _, err := m.promoteRegularsToTemp(context.Background(), ModelConfig{ModelID: "model-a"}, stale); err != nil {
		t.Fatalf("promoteRegularsToTemp() = %v, want nil", err)
	}

	row := testStore.devshards["reg-1"]
	if row.RotationRole != roleTemp {
		t.Errorf("rotation_role = %q, want the promotion applied", row.RotationRole)
	}
	if row.Active {
		t.Error("the escrow the chain says is gone was put back into routing")
	}
	if row.SettleTxHash != "SETTLE-TX" {
		t.Errorf("settle_tx_hash = %q, want the hash this tick broadcast kept", row.SettleTxHash)
	}
}

// Test flow:
//  1. Build a manager whose tx client refuses every create as underfunded, with a recording narrator.
//  2. Call fillToLabel for a model short of one temp three times.
//  3. Assert every call attempted the create, since the breaker never gated it, and the underfunded refusal was narrated once.
//  4. Let one create succeed, then refuse again; assert the refusal is narrated a second time.
func TestAnUnderfundedCreateIsRetriedEveryTickAndNarratedOncePerEpisode(t *testing.T) {
	narrator := &recordingLifecycleNarrator{}
	underfunded := true
	nextEscrowID := uint64(40)
	txClient := &fakeTxClient{createEscrowFn: func(_ context.Context, _ *signing.Secp256k1Signer, amount uint64, _ string, onPrepared func(string) error) (chain.CreateEscrowResult, error) {
		if underfunded {
			return chain.CreateEscrowResult{}, &chain.WalletUnderfundedError{Address: "gonka1wallet", Have: 5, Need: amount + 1}
		}
		nextEscrowID++
		txHash := fmt.Sprintf("TX-%d", nextEscrowID)
		if err := onPrepared(txHash); err != nil {
			return chain.CreateEscrowResult{}, err
		}
		return chain.CreateEscrowResult{EscrowID: nextEscrowID, TxHash: txHash}, nil
	}}
	m := &Manager{
		tx: txClient, store: newFakeStore(), signer: &fakeSignerSource{signer: testSigner(t)},
		breaker: newCreateBreaker(), now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
		narrator: narrator,
	}
	model := ModelConfig{ModelID: "model-a", TargetCount: 1, Amount: 100}
	snapshot := chain.PhaseSnapshot{EpochIndex: 3, BlockHeight: 10}

	for range 3 {
		err := m.fillToLabel(t.Context(), roleTemp, snapshot.EpochIndex, []ModelConfig{model}, func(ModelConfig) int { return 1 }, snapshot, nil)[0].err
		require.ErrorIs(t, err, chain.ErrWalletUnderfunded)
	}
	require.Equal(t, 3, txClient.createCalls, "an underfunded wallet must not open the create breaker")

	underfunded = false
	err := m.fillToLabel(t.Context(), roleTemp, snapshot.EpochIndex, []ModelConfig{model}, func(ModelConfig) int { return 1 }, snapshot, nil)[0].err
	require.NoError(t, err)
	underfunded = true
	err = m.fillToLabel(t.Context(), roleTemp, snapshot.EpochIndex, []ModelConfig{model}, func(ModelConfig) int { return 1 }, snapshot, nil)[0].err
	require.ErrorIs(t, err, chain.ErrWalletUnderfunded)

	refusals := 0
	for _, call := range narrator.recorded() {
		if call == "underfunded model-a temp have 5 need 101" {
			refusals++
		}
	}
	require.Equal(t, 2, refusals, "narrated: %v", narrator.recorded())
}

// Test flow:
//  1. Table-driven: a budget of 3 already full (no room), and a model the network does not serve; each with one serving regular and a temp count of 1, and a chain client that fails the test on any create.
//  2. Run prepareBridge.
//  3. Assert it reports no error, the regular is relabelled temp and still active, and the rotation status names the partial fill.
func TestABridgeShortOfItsTempsPromotesInsteadOfRetiring(t *testing.T) {
	testCases := []struct {
		name     string
		model    ModelConfig
		parked   int
		snapshot chain.PhaseSnapshot
	}{
		{
			name:     "no room under the budget",
			model:    ModelConfig{ModelID: "model-a", TempCount: 1, TargetCount: 1, Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY", MaxUnsettled: 3},
			parked:   2,
			snapshot: servedSnapshot(9, 500, "model-a"),
		},
		{
			name:     "the network does not serve the model",
			model:    ModelConfig{ModelID: "model-a", TempCount: 1, TargetCount: 1, Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"},
			snapshot: servedSnapshot(9, 500, "model-b"),
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			testStore := newFakeStore()
			regular := store.DevshardRecord{EscrowID: "reg-1", Model: "model-a", Active: true, RotationRole: roleRegular, RotationEpoch: 9, PrivateKeyEnv: "MODEL_A_KEY"}
			devshards := []store.DevshardRecord{regular}
			for index := range testCase.parked {
				parked := store.DevshardRecord{EscrowID: fmt.Sprintf("parked-%d", index), Model: "model-a", SettlementPending: true, PrivateKeyEnv: "MODEL_A_KEY"}
				devshards = append(devshards, parked)
			}
			for _, record := range devshards {
				testStore.devshards[record.EscrowID] = record
			}
			m := newRotationManager(t, testStore, &fakeTxClient{createEscrowFn: failOnCreate(t)}, false)

			if err := m.prepareBridge(context.Background(), testCase.snapshot, []ModelConfig{testCase.model}, devshards); err != nil {
				t.Fatalf("prepareBridge() = %v, want nil: a partial fill is recorded, not a failed tick", err)
			}

			if got := testStore.devshards["reg-1"]; !got.Active || got.RotationRole != roleTemp {
				t.Fatalf("reg-1 = %+v, want still active, relabelled temp", got)
			}
			if status := testStore.rotationStatuses["model-a|"+roleTemp]; !strings.Contains(status.CreateError, errBridgePartialFill.Error()) {
				t.Fatalf("rotation status create error = %q, want it to name the partial fill", status.CreateError)
			}
		})
	}
}
