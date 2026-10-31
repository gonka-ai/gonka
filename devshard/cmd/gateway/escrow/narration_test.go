package escrow

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
	"devshard/signing"
)

type recordingLifecycleNarrator struct {
	mu      sync.Mutex
	calls   []string
	reasons map[string]string
}

func (n *recordingLifecycleNarrator) note(format string, values ...any) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, fmt.Sprintf(format, values...))
}

func (n *recordingLifecycleNarrator) recorded() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.calls...)
}

func (n *recordingLifecycleNarrator) EscrowCreated(escrowID, model, role, reason string, epoch uint64, txHash string) {
	n.note("created %s %s %s epoch %d tx %s", escrowID, model, role, epoch, txHash)
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.reasons == nil {
		n.reasons = map[string]string{}
	}
	n.reasons[escrowID] = reason
}

func (n *recordingLifecycleNarrator) createdReasons() map[string]string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return maps.Clone(n.reasons)
}

func (n *recordingLifecycleNarrator) EscrowRecovered(escrowID, model, role string, epoch uint64, txHash string) {
	n.note("recovered %s %s %s epoch %d tx %s", escrowID, model, role, epoch, txHash)
}

func (n *recordingLifecycleNarrator) CommitmentCleared(txHash, model, role string, epoch uint64, reason string) {
	n.note("cleared %s %s %s epoch %d: %s", txHash, model, role, epoch, reason)
}

func (n *recordingLifecycleNarrator) EscrowGoneFromChain(escrowID string) {
	n.note("gone from chain %s", escrowID)
}

func (n *recordingLifecycleNarrator) EscrowDeadlineReached(escrowID string, chainEpoch uint64, settleBy int64) {
	n.note("deadline reached %s epoch %d settle by %d", escrowID, chainEpoch, settleBy)
}

func (n *recordingLifecycleNarrator) EscrowDeadlineUnsettled(escrowID string, settleBy int64, reason string) {
	n.note("deadline unsettled %s settle by %d: %s", escrowID, settleBy, reason)
}

func (n *recordingLifecycleNarrator) SettleMarginShort(marginBlocks int64, blockTime, need time.Duration) {
	n.note("settle margin short %d blocks of %s, need %s", marginBlocks, blockTime, need)
}

func (n *recordingLifecycleNarrator) EscrowDeadlinesProjected(height, projected int64, staleFor time.Duration) {
	n.note("deadlines projected from %d to %d after %s", height, projected, staleFor)
}

func (n *recordingLifecycleNarrator) EscrowChainFactsUnresolved(escrowID, reason string) {
	n.note("chain facts unresolved %s: %s", escrowID, reason)
}

func (n *recordingLifecycleNarrator) EscrowMarkedForReplacement(escrowID, reason string) {
	n.note("marked for replacement %s: %s", escrowID, reason)
}

func (n *recordingLifecycleNarrator) RotationSkipped(model, role string, epoch uint64) {
	n.note("rotation skipped %s %s epoch %d", model, role, epoch)
}

func (n *recordingLifecycleNarrator) RegularsPromotedToTemp(model string, epoch uint64, promoted int) {
	n.note("promoted %s epoch %d: %d", model, epoch, promoted)
}

func (n *recordingLifecycleNarrator) BridgePrepared(model string, epoch uint64, created, retired int) {
	n.note("bridge prepared %s epoch %d: created %d retired %d", model, epoch, created, retired)
}

func (n *recordingLifecycleNarrator) BridgeFinished(model string, epoch uint64, created, retired int) {
	n.note("bridge finished %s epoch %d: created %d retired %d", model, epoch, created, retired)
}

func (n *recordingLifecycleNarrator) EscrowParked(escrowID string) { n.note("parked %s", escrowID) }

func (n *recordingLifecycleNarrator) EscrowCreateUnderfunded(model, role string, have, need uint64) {
	n.note("underfunded %s %s have %d need %d", model, role, have, need)
}

func (n *recordingLifecycleNarrator) EscrowCreateBelowFloor(model, role string, amount, floor uint64) {
	n.note("below floor %s %s amount %d floor %d", model, role, amount, floor)
}

func (n *recordingLifecycleNarrator) EscrowReserveTaken(escrowID string) {
	n.note("reserve taken %s", escrowID)
}

func (n *recordingLifecycleNarrator) EscrowSettled(escrowID, model, txHash, settler string) {
	n.note("settled %s %s tx %s settler %s", escrowID, model, txHash, settler)
}

func (n *recordingLifecycleNarrator) SettlementReconciled(escrowID, txHash string) {
	n.note("reconciled %s tx %s", escrowID, txHash)
}

func (n *recordingLifecycleNarrator) SettledRecordDropped(escrowID string) {
	n.note("record dropped %s", escrowID)
}

func (n *recordingLifecycleNarrator) EscrowTickFailed(err error) { n.note("tick failed: %v", err) }

func (n *recordingLifecycleNarrator) TimeoutsSwept(due, applied, failed int) {
	n.note("swept due %d applied %d failed %d", due, applied, failed)
}

func (n *recordingLifecycleNarrator) EscrowPlanned(model string, creates, retires int, reasons string, moneyShort bool, need, liquid uint64, fullCount int) {
	n.note("planned %s creates %d retires %d reasons %s", model, creates, retires, reasons)
}

func (n *recordingLifecycleNarrator) EscrowStarved(escrowID string, free uint64) {
	n.note("starved %s free %d", escrowID, free)
}

func (n *recordingLifecycleNarrator) EscrowFull(escrowID string, free uint64) {
	n.note("full %s free %d", escrowID, free)
}

func (n *recordingLifecycleNarrator) FundingGuaranteeBroken(model string, have, want int) {
	n.note("guarantee broken %s have %d want %d", model, have, want)
}

func (n *recordingLifecycleNarrator) FundingMisconfigured(model string, amount, need uint64) {
	n.note("misconfigured %s amount %d need %d", model, amount, need)
}

func (n *recordingLifecycleNarrator) EscrowBudgetReached(model string, counted, maxUnsettled int) {
	n.note("budget reached %s counted %d max %d", model, counted, maxUnsettled)
}

func (n *recordingLifecycleNarrator) FundingPlanFailed(err error) { n.note("plan failed: %v", err) }

// Test flow:
//  1. Build a manager with a `recordingLifecycleNarrator` and a tx client whose createEscrowFn returns escrow ID 42.
//  2. Call createEscrow for a temp role at epoch 7.
//  3. Assert createEscrow returns no error.
//  4. Assert the narrator recorded the escrow ID as text ("42"), not the chain's numeric type.
func TestACreatedEscrowIsNarratedWithItsIDAsText(t *testing.T) {
	narrator := &recordingLifecycleNarrator{}
	txClient := &fakeTxClient{createEscrowFn: func(_ context.Context, _ *signing.Secp256k1Signer, _ uint64, _ string, onPrepared func(string) error) (chain.CreateEscrowResult, error) {
		if err := onPrepared("TX-HAPPY"); err != nil {
			return chain.CreateEscrowResult{}, err
		}
		return chain.CreateEscrowResult{EscrowID: 42, TxHash: "TX-HAPPY"}, nil
	}}
	m := &Manager{
		tx: txClient, store: newFakeStore(), signer: &fakeSignerSource{signer: testSigner(t)},
		breaker: newCreateBreaker(), now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
		narrator: narrator,
	}

	_, err := m.createFor(context.Background(), ModelConfig{ModelID: "model-a", Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}, roleTemp, 7, string(createdForBridge), chain.PhaseSnapshot{EpochIndex: 7, BlockHeight: 500})

	require.NoError(t, err)
	require.Equal(t, []string{"created 42 model-a temp epoch 7 tx TX-HAPPY"}, narrator.recorded())
}

// Test flow:
//  1. Save a commitment and stub getTxEscrowIDFn, varied across cases: a create that landed while the gateway was down, a transaction that committed and created nothing, and a transaction past its reconcile window.
//  2. Call reconcile.
//  3. Assert reconcile returns no error.
//  4. Assert the narrator recorded the expected "recovered" or "cleared" line for that case.
func TestReconcileNarratesWhatItRecoveredAndWhatItCleared(t *testing.T) {
	fixedNow := time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC)
	testCases := []struct {
		name            string
		commitment      store.Commitment
		getTxEscrowIDFn func(ctx context.Context, txHash string) (uint64, bool, error)
		want            []string
	}{
		{
			name:            "a create that landed while the gateway was down",
			commitment:      store.Commitment{TxHash: "TXA", Model: "model-a", Role: roleTemp, Epoch: 3, PrivateKeyEnv: "KEY_A", CreatedAt: fixedNow},
			getTxEscrowIDFn: func(context.Context, string) (uint64, bool, error) { return 55, true, nil },
			want:            []string{"recovered 55 model-a temp epoch 3 tx TXA"},
		},
		{
			name:            "a transaction that committed and created nothing",
			commitment:      store.Commitment{TxHash: "TXB", Model: "model-a", Role: roleTemp, CreatedAt: fixedNow},
			getTxEscrowIDFn: func(context.Context, string) (uint64, bool, error) { return 0, false, nil },
			want:            []string{"cleared TXB model-a temp epoch 0: " + commitmentClearedNoEscrow},
		},
		{
			name:            "a transaction past its window",
			commitment:      store.Commitment{TxHash: "TXD", Model: "model-a", Role: roleTemp, CreatedAt: fixedNow.Add(-commitmentReconcileGrace - time.Second)},
			getTxEscrowIDFn: func(context.Context, string) (uint64, bool, error) { return 0, false, chain.ErrTxNotFound },
			want:            []string{"cleared TXD model-a temp epoch 0: " + commitmentClearedCannotLand},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			testStore := newFakeStore()
			require.NoError(t, testStore.SaveCommitment(context.Background(), testCase.commitment))
			narrator := &recordingLifecycleNarrator{}
			m := &Manager{
				tx: &fakeTxClient{getTxEscrowIDFn: testCase.getTxEscrowIDFn}, store: testStore,
				breaker: newCreateBreaker(), now: func() time.Time { return fixedNow }, narrator: narrator,
			}

			require.NoError(t, m.reconcile(context.Background()))

			require.Equal(t, testCase.want, narrator.recorded())
		})
	}
}

// Test flow:
//  1. Store one active devshard record and a tx client whose GetEscrow reports it not found.
//  2. Call TriggerEscrowCheck for that escrow and assert it returns no error.
//  3. Assert the narrator recorded "gone from chain 1".
func TestAnEscrowGoneFromChainIsNarrated(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["1"] = store.DevshardRecord{EscrowID: "1", Model: "model-a", Active: true}
	narrator := &recordingLifecycleNarrator{}
	m := &Manager{
		tx: &fakeTxClient{getEscrowFn: func(context.Context, string) (chain.EscrowInfo, bool, error) {
			return chain.EscrowInfo{}, false, nil
		}},
		store: testStore, settlementSource: &fakeSettlementSource{}, narrator: narrator,
	}

	require.NoError(t, m.TriggerEscrowCheck(context.Background(), "1"))

	require.Equal(t, []string{"gone from chain 1"}, narrator.recorded())
}

// Test flow:
//  1. Call OnBalanceExhausted for the same escrow ID twice with the same reason.
//  2. Assert the narrator recorded the "marked for replacement" line only once.
func TestADepletedEscrowIsNarratedOnceWhenMarked(t *testing.T) {
	narrator := &recordingLifecycleNarrator{}
	m := &Manager{narrator: narrator}

	m.OnBalanceExhausted("1", scheduler.ExhaustionNonceCap)
	m.OnBalanceExhausted("1", scheduler.ExhaustionNonceCap)

	require.Equal(t, []string{"marked for replacement 1: nonce_cap"}, narrator.recorded())
}

// Test flow:
//  1. Build a manager with a `recordingLifecycleNarrator` and a snapshot where the target model has no served weight.
//  2. Call fillToLabel for that model and role.
//  3. Assert it returns no error and created zero escrows.
//  4. Assert the narrator recorded the "rotation skipped" line, since that is the only record of why nothing was created.
func TestASkippedRotationIsNarrated(t *testing.T) {
	narrator := &recordingLifecycleNarrator{}
	manager := &Manager{narrator: narrator}
	snapshot := chain.PhaseSnapshot{
		EpochIndex:            4,
		FullWeightsByModel:    map[string]map[string]float64{"other-model": {"gonka1host": 1}},
		CurrentWeightsByModel: map[string]map[string]float64{"other-model": {"gonka1host": 1}},
	}

	fill := manager.fillToLabel(context.Background(), roleRegular, snapshot.EpochIndex, []ModelConfig{{ModelID: "qwen"}}, func(ModelConfig) int { return 1 }, snapshot, nil)[0]

	require.NoError(t, fill.err)
	require.Zero(t, fill.created)
	require.Equal(t, []string{"rotation skipped qwen regular epoch 4"}, narrator.recorded())
}

// Test flow:
//  1. Build a rotation manager with one active regular record and a createEscrowFn that broadcasts and then fails.
//  2. Call prepareBridge for that model.
//  3. Assert prepareBridge returns an error.
//  4. Assert the narrator recorded the regular-to-temp promotion despite the failure.
func TestAFailedBridgePreparationNarratesThePromotion(t *testing.T) {
	testStore := newFakeStore()
	regular := store.DevshardRecord{EscrowID: "reg-1", Model: "model-a", Active: true, RotationRole: roleRegular, PrivateKeyEnv: "MODEL_A_KEY"}
	testStore.devshards[regular.EscrowID] = regular
	txClient := &fakeTxClient{createEscrowFn: func(_ context.Context, _ *signing.Secp256k1Signer, _ uint64, _ string, onPrepared func(string) error) (chain.CreateEscrowResult, error) {
		if err := onPrepared("TX-FAIL"); err != nil {
			return chain.CreateEscrowResult{}, err
		}
		return chain.CreateEscrowResult{}, errors.New("broadcast rejected")
	}}
	m := newRotationManager(t, testStore, txClient, false)
	narrator := &recordingLifecycleNarrator{}
	m.narrator = narrator
	models := []ModelConfig{{ModelID: "model-a", TempCount: 1, TargetCount: 3, Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}}

	require.Error(t, m.prepareBridge(context.Background(), servedSnapshot(9, 500, "model-a"), models, []store.DevshardRecord{regular}))

	require.Equal(t, []string{"promoted model-a epoch 9: 1"}, narrator.recorded())
}

// Test flow:
//  1. Build a rotation manager with two active regular records and a succeeding createEscrowFn.
//  2. Call prepareBridge for that model and assert it returns no error.
//  3. Assert the narrator recorded, in order, the new temp escrow's creation, both regulars being parked, and the bridge-prepared summary.
func TestAPreparedBridgeNarratesWhatItCreatedAndRetired(t *testing.T) {
	testStore := newFakeStore()
	regularOne := store.DevshardRecord{EscrowID: "reg-1", Model: "model-a", Active: true, RotationRole: roleRegular, PrivateKeyEnv: "MODEL_A_KEY"}
	regularTwo := store.DevshardRecord{EscrowID: "reg-2", Model: "model-a", Active: true, PrivateKeyEnv: "MODEL_A_KEY"}
	testStore.devshards[regularOne.EscrowID] = regularOne
	testStore.devshards[regularTwo.EscrowID] = regularTwo
	m := newRotationManager(t, testStore, &fakeTxClient{createEscrowFn: succeedingCreateEscrowFn(200)}, false)
	narrator := &recordingLifecycleNarrator{}
	m.narrator = narrator
	models := []ModelConfig{{ModelID: "model-a", TempCount: 1, TargetCount: 3, Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}}

	require.NoError(t, m.prepareBridge(context.Background(), servedSnapshot(9, 500, "model-a"), models, []store.DevshardRecord{regularOne, regularTwo}))

	require.Equal(t, []string{
		"created 200 model-a temp epoch 10 tx TX-200",
		"parked reg-1",
		"parked reg-2",
		"bridge prepared model-a epoch 9: created 1 retired 2",
	}, narrator.recorded())
}

// Test flow:
//  1. Build a manager with one active record, settlement enabled, and a settleEscrowFn that succeeds.
//  2. Call retire for that record and assert it returns no error.
//  3. Assert the narrator recorded, in order, the park, the settlement, and the record drop.
func TestASettledRetirementIsNarratedFromParkToDrop(t *testing.T) {
	testStore := newFakeStore()
	record := store.DevshardRecord{EscrowID: "5", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	testStore.devshards[record.EscrowID] = record
	txClient := &fakeTxClient{settleEscrowFn: func(_ context.Context, _ *signing.Secp256k1Signer, input chain.SettlementInput) (chain.SettleEscrowResult, error) {
		return chain.SettleEscrowResult{EscrowID: input.EscrowID, TxHash: "SETTLE-TX", Settler: "gonka1settler"}, nil
	}}
	narrator := &recordingLifecycleNarrator{}
	m := &Manager{
		tx: txClient, store: testStore, signer: &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{}, config: holderWithSettlementEnabled(true), narrator: narrator,
	}

	require.NoError(t, m.retire(context.Background(), record))

	require.Equal(t, []string{
		"parked 5",
		"settled 5 model-a tx SETTLE-TX settler gonka1settler",
		"record dropped 5",
	}, narrator.recorded())
}

// Test flow:
//  1. Store a record already SettlementPending with a settle tx hash, and a tx client whose txCommittedFn reports the transaction committed.
//  2. Call settle unforced for that record and assert it returns no error.
//  3. Assert the narrator recorded the reconciled line alone: the record was already parked, so no second park is narrated.
func TestASettleAlreadyOnChainIsNarratedAsReconciled(t *testing.T) {
	record := store.DevshardRecord{EscrowID: "7", Model: "model-a", SettlementPending: true, SettleTxHash: "SETTLE-TX"}
	testStore := newFakeStore()
	testStore.devshards[record.EscrowID] = record
	narrator := &recordingLifecycleNarrator{}
	m := &Manager{
		tx:    &fakeTxClient{txCommittedFn: func(string) (bool, error) { return true, nil }},
		store: testStore, signer: &fakeSignerSource{signer: testSigner(t)},
		settlementSource: &fakeSettlementSource{}, narrator: narrator,
	}

	_, err := m.settle(context.Background(), record, false)

	require.NoError(t, err)
	require.Equal(t, []string{"reconciled 7 tx SETTLE-TX"}, narrator.recorded())
}

// Test flow:
//  1. Build a manager whose store fails ListDevshards with a fixed error.
//  2. Call runTick.
//  3. Assert the narrator recorded a "tick failed" line naming that error.
func TestAFailedTickIsNarratedWithItsError(t *testing.T) {
	testStore := newFakeStore()
	testStore.listDevshardsErr = errors.New("store unavailable")
	cfg := config.Defaults()
	narrator := &recordingLifecycleNarrator{}
	deps := testManagerDeps(t, testStore, &fakeTxClient{}, &fakeSnapshotSource{}, &cfg)
	deps.Narrator = narrator
	m := mustManager(t, deps)

	m.runTick(context.Background(), true)

	require.Equal(t, []string{"tick failed: store unavailable"}, narrator.recorded())
}

// Test flow:
//  1. Configure a sweep budget and build a manager with a `recordingSweeper` (default fields: reports 1 due, 1 applied, 0 failed).
//  2. Call sweepTimeouts and wait for the sweep work to finish.
//  3. Assert the narrator recorded the "swept due 1 applied 1 failed 0" line.
func TestASweepThatFoundVotesOwedIsNarrated(t *testing.T) {
	cfg := config.Defaults()
	cfg.TimeoutSweep.BudgetPerTick = 3
	narrator := &recordingLifecycleNarrator{}
	deps := testManagerDeps(t, newFakeStore(), &fakeTxClient{}, &fakeSnapshotSource{}, &cfg)
	deps.Timeouts = &recordingSweeper{}
	deps.Narrator = narrator
	m := mustManager(t, deps)

	m.sweepTimeouts(context.Background())
	m.offTickWork.Wait()

	require.Equal(t, []string{"swept due 1 applied 1 failed 0"}, narrator.recorded())
}
