package scenarios

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/testenv/mockchain/txexec"
)

var errFakeChainScripted = errors.New("fake chain: scripted failure")

type chainOperation string

const (
	operationBroadcastCreate chainOperation = "broadcast_create"
	operationBroadcastSettle chainOperation = "broadcast_settle"
	operationEscrowLookup    chainOperation = "escrow_lookup"
)

const (
	fakeChainID             = "scenario-chain"
	chainCodeRejected       = 5
	chainCodeOutOfWindow    = 6
	chainCodeWrongSettler   = 7
	chainCodeOverpaid       = 8
	chainCodeRemainderFees  = 9
	chainCodeSlotOutOfRange = 10
)

type chainEpoch struct {
	latest           uint64
	effective        uint64
	blockHeight      int64
	pocStart         int64
	setNewValidators int64
	phase            chain.EpochPhase
}

type chainEscrow struct {
	id           uint64
	creator      string
	model        string
	amount       uint64
	epochIndex   uint64
	slots        []string
	settled      bool
	pruned       bool
	settledCosts uint64
	settledFees  uint64
	refund       uint64
	settledAt    time.Time
}

type chainCreate struct {
	escrowID uint64
	model    string
	at       time.Time
}

// settledSlot is one host_stats entry of a settle: the slot it names and the cost it carries.
type settledSlot struct {
	slot uint32
	cost uint64
}

type fakeChainShape struct {
	participants []string
	groupSize    int
	params       chain.EscrowParams
	models       map[string]chain.ModelParams
	wallets      map[string]uint64
	txFee        uint64
	epoch        chainEpoch
	slotOwners   []int
}

// fakeChain is the chain a scenario runs against: wallet, escrows, epochs, pruning and scripted failures.
type fakeChain struct {
	mu              sync.Mutex
	shape           fakeChainShape
	wallets         map[string]uint64
	escrows         map[uint64]*chainEscrow
	nextEscrowID    uint64
	committed       map[string]chain.TxResult
	failures        map[chainOperation]int
	lostResponses   map[chainOperation]int
	neverCommits    map[chainOperation]int
	broadcasts      map[chainOperation]int
	lookupsDown     bool
	hiddenEffective bool
	creates         []chainCreate
	epoch           chainEpoch
	createHook      func(chainCreate)
	blockTime       time.Duration
	heightAnchor    int64
	heightAnchorAt  time.Time
	apiDown         bool
	createsRejected bool
	confirmationPoC bool
}

func newFakeChain(shape fakeChainShape) *fakeChain {
	wallets := make(map[string]uint64, len(shape.wallets))
	maps.Copy(wallets, shape.wallets)
	return &fakeChain{
		shape:         shape,
		wallets:       wallets,
		escrows:       map[uint64]*chainEscrow{},
		nextEscrowID:  1000,
		committed:     map[string]chain.TxResult{},
		failures:      map[chainOperation]int{},
		lostResponses: map[chainOperation]int{},
		neverCommits:  map[chainOperation]int{},
		broadcasts:    map[chainOperation]int{},
		epoch:         shape.epoch,
	}
}

func (c *fakeChain) EscrowParams(context.Context) (chain.EscrowParams, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.shape.params, true, nil
}

func (c *fakeChain) PreservedNodes(context.Context) (*chain.PreservedNodes, bool, error) {
	return nil, false, nil
}

func (c *fakeChain) Models(context.Context) (map[string]chain.ModelParams, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.shape.models), nil
}

func (c *fakeChain) ChainID(context.Context) (string, error) { return fakeChainID, nil }

func (c *fakeChain) Account(context.Context, string) (chain.Account, error) {
	return chain.Account{Number: 1}, nil
}

func (c *fakeChain) SpendableBalance(_ context.Context, address, _ string) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wallets[address], nil
}

func (c *fakeChain) Broadcast(_ context.Context, txBytes []byte) (string, error) {
	messages, err := txexec.DecodeTxMessages(txBytes)
	if err != nil {
		return "", err
	}
	operation := operationBroadcastSettle
	if len(messages) > 0 && messages[0].Create != nil {
		operation = operationBroadcastCreate
	}
	hash := fakeTxHash(txBytes)
	c.mu.Lock()
	c.broadcasts[operation]++
	if c.takeLocked(c.failures, operation) {
		c.mu.Unlock()
		return "", errFakeChainScripted
	}
	if c.takeLocked(c.neverCommits, operation) {
		c.mu.Unlock()
		return hash, nil
	}
	var created *chainCreate
	result := chain.TxResult{}
	for _, message := range messages {
		switch {
		case message.Create != nil:
			result, created = c.applyCreateLocked(message.Create.Creator, message.Create.ModelId, message.Create.Amount)
		case message.Settle != nil:
			slots := make([]settledSlot, 0, len(message.Settle.HostStats))
			for _, stats := range message.Settle.HostStats {
				slots = append(slots, settledSlot{slot: stats.SlotId, cost: stats.Cost})
			}
			result = c.applySettleLocked(message.Settle.Settler, message.Settle.EscrowId, slots, message.Settle.Fees)
		}
	}
	c.committed[hash] = result
	responseLost := c.takeLocked(c.lostResponses, operation)
	hook := c.createHook
	c.mu.Unlock()
	if created != nil && hook != nil {
		hook(*created)
	}
	if responseLost {
		return "", errFakeChainScripted
	}
	return hash, nil
}

func (c *fakeChain) Tx(_ context.Context, txHash string) (chain.TxResult, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	result, found := c.committed[strings.ToUpper(txHash)]
	return result, found, nil
}

func (c *fakeChain) Escrow(_ context.Context, escrowID uint64) (chain.EscrowInfo, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lookupsDown || c.takeLocked(c.failures, operationEscrowLookup) {
		return chain.EscrowInfo{}, false, errFakeChainScripted
	}
	record, known := c.escrows[escrowID]
	if !known || record.pruned {
		return chain.EscrowInfo{}, false, nil
	}
	return chain.EscrowInfo{EscrowID: strconv.FormatUint(escrowID, 10), Balance: record.amount, EpochIndex: record.epochIndex}, true, nil
}

func (c *fakeChain) fundEscrow(creator, model string, amount uint64) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recordEscrowLocked(creator, model, amount).id
}

func (c *fakeChain) applyCreateLocked(creator, model string, amount uint64) (chain.TxResult, *chainCreate) {
	if c.createsRejected {
		return chain.TxResult{Code: chainCodeRejected, RawLog: "no model weights for this epoch"}, nil
	}
	if c.wallets[creator] < amount+c.shape.txFee {
		return chain.TxResult{Code: chainCodeRejected, RawLog: "insufficient funds"}, nil
	}
	c.wallets[creator] -= amount + c.shape.txFee
	record := c.recordEscrowLocked(creator, model, amount)
	created := chainCreate{escrowID: record.id, model: model, at: time.Now()}
	c.creates = append(c.creates, created)
	return chain.TxResult{Events: []chain.TxEvent{{
		Type:       "devshard_escrow_created",
		Attributes: []chain.TxAttribute{{Key: "escrow_id", Value: strconv.FormatUint(record.id, 10)}},
	}}}, &created
}

func (c *fakeChain) recordEscrowLocked(creator, model string, amount uint64) *chainEscrow {
	c.nextEscrowID++
	var slots []string
	if len(c.shape.slotOwners) > 0 {
		slots = make([]string, 0, len(c.shape.slotOwners))
		for _, owner := range c.shape.slotOwners {
			slots = append(slots, c.shape.participants[owner])
		}
	} else {
		slots = append(slots, c.shape.participants[:c.shape.groupSize]...)
	}
	record := &chainEscrow{id: c.nextEscrowID, creator: creator, model: model, amount: amount, epochIndex: c.epoch.effective, slots: slots}
	c.escrows[record.id] = record
	return record
}

// applySettleLocked pays as the keeper does: each named slot its cost and an equal share of the fees, one remainder coin per named slot in order, and the creator the rest.
func (c *fakeChain) applySettleLocked(settler string, escrowID uint64, slots []settledSlot, fees uint64) chain.TxResult {
	record, known := c.escrows[escrowID]
	switch {
	case !known || record.settled || record.pruned:
		return chain.TxResult{Code: chainCodeRejected, RawLog: "escrow not found"}
	case record.creator != settler:
		return chain.TxResult{Code: chainCodeWrongSettler, RawLog: "settler is not the creator"}
	case c.epoch.effective != record.epochIndex && c.epoch.effective != record.epochIndex+1:
		return chain.TxResult{Code: chainCodeOutOfWindow, RawLog: "outside the settlement window"}
	}
	totalSlots := uint64(len(record.slots))
	feePerSlot, remainderFees := fees/totalSlots, fees%totalSlots
	var totalCost, payout uint64
	for _, named := range slots {
		if int(named.slot) >= len(record.slots) {
			return chain.TxResult{Code: chainCodeSlotOutOfRange, RawLog: fmt.Sprintf("host_stats slot_id %d out of range", named.slot)}
		}
		totalCost += named.cost
		payout += named.cost + feePerSlot
		if remainderFees > 0 {
			payout++
			remainderFees--
		}
	}
	if remainderFees != 0 {
		return chain.TxResult{Code: chainCodeRemainderFees, RawLog: fmt.Sprintf("failed to allocate all remainder fees, %d left", remainderFees)}
	}
	if payout > record.amount {
		return chain.TxResult{Code: chainCodeOverpaid, RawLog: "payout exceeds amount"}
	}
	refund := record.amount - payout
	c.wallets[record.creator] += refund
	record.settled, record.settledCosts, record.settledFees, record.refund, record.settledAt = true, totalCost, fees, refund, time.Now()
	return chain.TxResult{}
}

func (c *fakeChain) startPoC() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch.latest++
	c.setHeightLocked(c.epoch.pocStart)
	c.epoch.phase = chain.EpochPhasePoCGenerate
}

func (c *fakeChain) moveToHeight(height int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setHeightLocked(height)
}

// reachSetNewValidators moves the chain to exactly the switch height in the given phase, where the reply still names that height as the latest epoch's set_new_validators.
func (c *fakeChain) reachSetNewValidators(phase chain.EpochPhase) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch.effective = c.epoch.latest
	c.setHeightLocked(c.epoch.setNewValidators)
	c.epoch.phase = phase
}

func (c *fakeChain) setNewValidators(nextPoCStart, nextSwitch int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch.effective = c.epoch.latest
	c.setHeightLocked(c.epoch.setNewValidators)
	c.epoch.phase = chain.EpochPhaseInference
	c.epoch.pocStart, c.epoch.setNewValidators = nextPoCStart, nextSwitch
	for _, record := range c.escrows {
		if !record.pruned && c.epoch.effective >= record.epochIndex+2 {
			record.pruned = true
		}
	}
}

func (c *fakeChain) failNext(operation chainOperation, times int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures[operation] += times
}

func (c *fakeChain) loseNextResponse(operation chainOperation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lostResponses[operation]++
}

func (c *fakeChain) neverCommitNext(operation chainOperation, times int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.neverCommits[operation] += times
}

func (c *fakeChain) broadcastCount(operation chainOperation) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.broadcasts[operation]
}

func (c *fakeChain) setLookupsDown(down bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lookupsDown = down
}

// hideEffectiveEpoch makes the public API omit the latest epoch's PoC start, so the observer cannot derive the effective epoch.
func (c *fakeChain) hideEffectiveEpoch() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hiddenEffective = true
}

func (c *fakeChain) effectiveHidden() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hiddenEffective
}

func (c *fakeChain) setCreateHook(hook func(chainCreate)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.createHook = hook
}

func (c *fakeChain) escrowRecord(escrowID uint64) (chainEscrow, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record, known := c.escrows[escrowID]
	if !known {
		return chainEscrow{}, false
	}
	copied := *record
	copied.slots = append([]string(nil), record.slots...)
	return copied, true
}

func (c *fakeChain) createsOf(model string) []chainCreate {
	c.mu.Lock()
	defer c.mu.Unlock()
	var creates []chainCreate
	for _, created := range c.creates {
		if created.model == model {
			creates = append(creates, created)
		}
	}
	return creates
}

func (c *fakeChain) escrowsOf(model string) []chainEscrow {
	c.mu.Lock()
	defer c.mu.Unlock()
	var records []chainEscrow
	for _, record := range c.escrows {
		if record.model == model {
			records = append(records, *record)
		}
	}
	return records
}

func (c *fakeChain) snapshotEpoch() chainEpoch {
	c.mu.Lock()
	defer c.mu.Unlock()
	epoch := c.epoch
	epoch.blockHeight = c.heightLocked()
	return epoch
}

func (c *fakeChain) heightLocked() int64 {
	if c.blockTime <= 0 {
		return c.epoch.blockHeight
	}
	return c.heightAnchor + int64(time.Since(c.heightAnchorAt)/c.blockTime)
}

func (c *fakeChain) setHeightLocked(height int64) {
	c.epoch.blockHeight, c.heightAnchor, c.heightAnchorAt = height, height, time.Now()
}

// advanceHeightsEvery grows the height by one block per blockTime of bubble time from now on; scripted moves re-anchor it.
func (c *fakeChain) advanceHeightsEvery(blockTime time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setHeightLocked(c.heightLocked())
	c.blockTime = blockTime
}

func (c *fakeChain) setPublicAPIDown(down bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiDown = down
}

func (c *fakeChain) publicAPIDown() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.apiDown
}

func (c *fakeChain) setConfirmationPoC(active bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.confirmationPoC = active
}

func (c *fakeChain) confirmationPoCActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.confirmationPoC
}

func (c *fakeChain) rejectCreates(rejected bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.createsRejected = rejected
}

func (c *fakeChain) setTokenPrice(price uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shape.params.TokenPrice = price
}

func (c *fakeChain) setModelLength(model string, length uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shape.models[model] = chain.ModelParams{ContextWindow: length, MaxModelLen: length}
}

// dropEscrow makes the chain stop holding an escrow without settling it, as a prune would.
func (c *fakeChain) dropEscrow(escrowID uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if record, known := c.escrows[escrowID]; known {
		record.pruned = true
	}
}

func (c *fakeChain) addToWallet(address string, amount uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wallets[address] += amount
}

func (c *fakeChain) takeLocked(counters map[chainOperation]int, operation chainOperation) bool {
	if counters[operation] == 0 {
		return false
	}
	counters[operation]--
	return true
}

func fakeTxHash(txBytes []byte) string {
	sum := sha256.Sum256(txBytes)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

const creatorAddress = "gonka1scenariocreator"

func testFakeChain(epoch chainEpoch) *fakeChain {
	return newFakeChain(fakeChainShape{
		participants: []string{"host-a", "host-b", "host-c", "host-d"},
		groupSize:    4,
		params:       chain.EscrowParams{MaxNonce: 1_000_000, TokenPrice: 1, FeePerNonce: 10, CreateDevshardFee: 100},
		models:       map[string]chain.ModelParams{"scenario-model": {ContextWindow: 8192, MaxModelLen: 8192}},
		wallets:      map[string]uint64{creatorAddress: 10_000_000},
		txFee:        1_000,
		epoch:        epoch,
	})
}

func inferenceEpoch() chainEpoch {
	return chainEpoch{latest: 7, effective: 7, blockHeight: 1000, pocStart: 2000, setNewValidators: 2100, phase: chain.EpochPhaseInference}
}

// Test flow:
//  1. Fund an escrow directly on a chain at effective epoch 7.
//  2. Assert the escrow is found with its amount and epoch 7, and its slots are the first four participants.
func TestFakeChainHoldsAFundedEscrow(t *testing.T) {
	blockchain := testFakeChain(inferenceEpoch())
	escrowID := blockchain.fundEscrow(creatorAddress, "scenario-model", 50_000)

	info, found, err := blockchain.Escrow(context.Background(), escrowID)
	if err != nil || !found {
		t.Fatalf("Escrow(%d) = found %v, err %v, want found", escrowID, found, err)
	}
	if info.Balance != 50_000 || info.EpochIndex != 7 {
		t.Fatalf("Escrow(%d) = %+v, want balance 50000 epoch 7", escrowID, info)
	}
	record, _ := blockchain.escrowRecord(escrowID)
	if len(record.slots) != 4 || record.slots[0] != "host-a" {
		t.Fatalf("escrowRecord(%d).slots = %v, want the four participants in order", escrowID, record.slots)
	}
}

// Test flow:
//  1. Fund an escrow at epoch 7, then move the chain through PoC and two validator switches.
//  2. Assert the escrow is pruned at effective epoch 9 and no longer found.
func TestFakeChainPrunesAnUnsettledEscrowAtEpochPlusTwo(t *testing.T) {
	blockchain := testFakeChain(inferenceEpoch())
	escrowID := blockchain.fundEscrow(creatorAddress, "scenario-model", 50_000)

	blockchain.startPoC()
	blockchain.setNewValidators(3000, 3100)
	if _, found, _ := blockchain.Escrow(context.Background(), escrowID); !found {
		t.Fatalf("Escrow(%d) at effective 8 = not found, want found", escrowID)
	}
	blockchain.startPoC()
	blockchain.setNewValidators(4000, 4100)

	if _, found, _ := blockchain.Escrow(context.Background(), escrowID); found {
		t.Fatalf("Escrow(%d) at effective 9 = found, want pruned", escrowID)
	}
	if record, _ := blockchain.escrowRecord(escrowID); !record.pruned {
		t.Fatalf("escrowRecord(%d).pruned = false, want true", escrowID)
	}
}

// Test flow:
//  1. Fund an escrow at epoch 7 and move the chain to effective epoch 9 without pruning it first.
//  2. Apply a settle for it.
//  3. Assert the settle is refused with a non-zero code and the escrow is not marked settled.
func TestFakeChainRefusesASettleOutsideItsWindow(t *testing.T) {
	blockchain := testFakeChain(inferenceEpoch())
	escrowID := blockchain.fundEscrow(creatorAddress, "scenario-model", 50_000)
	blockchain.mu.Lock()
	blockchain.epoch.effective = 9
	result := blockchain.applySettleLocked(creatorAddress, escrowID, []settledSlot{{slot: 0, cost: 100}}, 200)
	blockchain.mu.Unlock()

	if result.Code == 0 {
		t.Fatalf("applySettleLocked(epoch 7 at effective 9).Code = 0, want non-zero")
	}
	if record, _ := blockchain.escrowRecord(escrowID); record.settled {
		t.Fatalf("escrowRecord(%d).settled = true, want false", escrowID)
	}
}

// Test flow:
//  1. Fund an escrow at epoch 7 and settle it inside its window naming all four slots, with costs 100 and 200 and fees 200.
//  2. Assert the creator's wallet grows by amount minus costs minus fees, and the escrow is still found, marked settled.
//  3. Move the chain through two validator switches.
//  4. Assert the settled escrow is pruned and no longer found.
func TestFakeChainRefundsAmountMinusCostsAndFees(t *testing.T) {
	blockchain := testFakeChain(inferenceEpoch())
	escrowID := blockchain.fundEscrow(creatorAddress, "scenario-model", 50_000)
	before, _ := blockchain.SpendableBalance(context.Background(), creatorAddress, "ngonka")
	blockchain.mu.Lock()
	result := blockchain.applySettleLocked(creatorAddress, escrowID, []settledSlot{{slot: 0, cost: 100}, {slot: 1, cost: 200}, {slot: 2}, {slot: 3}}, 200)
	blockchain.mu.Unlock()

	after, _ := blockchain.SpendableBalance(context.Background(), creatorAddress, "ngonka")
	if result.Code != 0 || after-before != 50_000-300-200 {
		t.Fatalf("settle refund = %d (code %d), want %d", after-before, result.Code, 50_000-300-200)
	}
	if _, found, _ := blockchain.Escrow(context.Background(), escrowID); !found {
		t.Fatalf("Escrow(%d) after settle = not found, want found until pruned", escrowID)
	}
	if record, _ := blockchain.escrowRecord(escrowID); !record.settled {
		t.Fatalf("escrowRecord(%d).settled = false, want true", escrowID)
	}

	blockchain.startPoC()
	blockchain.setNewValidators(3000, 3100)
	blockchain.startPoC()
	blockchain.setNewValidators(4000, 4100)
	if _, found, _ := blockchain.Escrow(context.Background(), escrowID); found {
		t.Fatalf("Escrow(%d) after two switches = found, want pruned", escrowID)
	}
}

// Test flow:
//  1. Script the next escrow lookup to fail once.
//  2. Assert the first lookup errors and the second answers.
func TestFakeChainFailsAScriptedLookupOnce(t *testing.T) {
	blockchain := testFakeChain(inferenceEpoch())
	escrowID := blockchain.fundEscrow(creatorAddress, "scenario-model", 50_000)
	blockchain.failNext(operationEscrowLookup, 1)

	if _, _, err := blockchain.Escrow(context.Background(), escrowID); err == nil {
		t.Fatalf("Escrow(%d) first = nil error, want the scripted failure", escrowID)
	}
	if _, found, err := blockchain.Escrow(context.Background(), escrowID); err != nil || !found {
		t.Fatalf("Escrow(%d) second = found %v err %v, want found", escrowID, found, err)
	}
}

// Test flow:
//  1. Fund an escrow at epoch 7 over four slots and settle it naming only slots 0 and 1, with costs 100 and 200 and fees 200.
//  2. Assert each named slot took a quarter of the fees, so the creator's refund keeps the two unnamed slots' shares: 50000 - 300 - 100.
func TestFakeChainPaysTheFeesOnlyToTheSlotsASettleNames(t *testing.T) {
	blockchain := testFakeChain(inferenceEpoch())
	escrowID := blockchain.fundEscrow(creatorAddress, "scenario-model", 50_000)
	before, _ := blockchain.SpendableBalance(context.Background(), creatorAddress, "ngonka")
	blockchain.mu.Lock()
	result := blockchain.applySettleLocked(creatorAddress, escrowID, []settledSlot{{slot: 0, cost: 100}, {slot: 1, cost: 200}}, 200)
	blockchain.mu.Unlock()

	after, _ := blockchain.SpendableBalance(context.Background(), creatorAddress, "ngonka")
	if result.Code != 0 || after-before != 50_000-300-100 {
		t.Fatalf("settle refund = %d (code %d), want %d", after-before, result.Code, 50_000-300-100)
	}
	if record, _ := blockchain.escrowRecord(escrowID); record.refund != 50_000-300-100 || record.settledFees != 200 {
		t.Fatalf("escrowRecord(%d) refund %d fees %d, want refund %d and the requested fees 200", escrowID, record.refund, record.settledFees, 50_000-300-100)
	}
}

// Test flow:
//  1. Fund an escrow at epoch 7 over four slots and settle it naming one slot, with fees 7: one coin per slot and three remainder coins.
//  2. Assert the settle is refused, because the one named slot can take only one remainder coin, and the escrow is not marked settled.
func TestFakeChainRefusesASettleWhoseRemainderFeesFindNoSlot(t *testing.T) {
	blockchain := testFakeChain(inferenceEpoch())
	escrowID := blockchain.fundEscrow(creatorAddress, "scenario-model", 50_000)
	blockchain.mu.Lock()
	result := blockchain.applySettleLocked(creatorAddress, escrowID, []settledSlot{{slot: 0, cost: 100}}, 7)
	blockchain.mu.Unlock()

	if result.Code != chainCodeRemainderFees {
		t.Fatalf("applySettleLocked(fees 7, one slot).Code = %d, want %d", result.Code, chainCodeRemainderFees)
	}
	if record, _ := blockchain.escrowRecord(escrowID); record.settled {
		t.Fatalf("escrowRecord(%d).settled = true, want false", escrowID)
	}
}

// Test flow:
//  1. Fund an escrow at epoch 7 and settle it naming slot 4 of a four-slot group.
//  2. Assert the settle is refused as out of range.
func TestFakeChainRefusesASettleNamingASlotOutsideTheGroup(t *testing.T) {
	blockchain := testFakeChain(inferenceEpoch())
	escrowID := blockchain.fundEscrow(creatorAddress, "scenario-model", 50_000)
	blockchain.mu.Lock()
	result := blockchain.applySettleLocked(creatorAddress, escrowID, []settledSlot{{slot: 4, cost: 100}}, 0)
	blockchain.mu.Unlock()

	if result.Code != chainCodeSlotOutOfRange {
		t.Fatalf("applySettleLocked(slot 4).Code = %d, want %d", result.Code, chainCodeSlotOutOfRange)
	}
}

// Test flow:
//  1. Fund an escrow and take the chain's escrow lookups down.
//  2. Assert a lookup fails; bring the lookups back up and assert it answers with the escrow.
func TestFakeChainLookupsCanBeTakenDown(t *testing.T) {
	blockchain := testFakeChain(inferenceEpoch())
	escrowID := blockchain.fundEscrow(creatorAddress, "scenario-model", 50_000)
	blockchain.setLookupsDown(true)

	if _, _, err := blockchain.Escrow(context.Background(), escrowID); err == nil {
		t.Fatalf("Escrow(%d) with lookups down = nil error, want a failure", escrowID)
	}
	blockchain.setLookupsDown(false)
	if _, found, err := blockchain.Escrow(context.Background(), escrowID); err != nil || !found {
		t.Fatalf("Escrow(%d) with lookups up = found %v err %v, want found", escrowID, found, err)
	}
}

// Test flow:
//  1. Fund an escrow, then set the token price to 3 and the model's length to 4096, reject creates, add 500 to the creator's wallet and drop the escrow.
//  2. Assert the escrow params and models read the new price and length, a create is refused with the wallet untouched, the wallet grew by 500, and the dropped escrow is no longer found.
func TestFakeChainKnobsChangeWhatTheChainAnswers(t *testing.T) {
	blockchain := testFakeChain(inferenceEpoch())
	escrowID := blockchain.fundEscrow(creatorAddress, "scenario-model", 50_000)
	blockchain.setTokenPrice(3)
	blockchain.setModelLength("scenario-model", 4096)
	blockchain.rejectCreates(true)
	blockchain.addToWallet(creatorAddress, 500)
	blockchain.dropEscrow(escrowID)

	params, _, _ := blockchain.EscrowParams(context.Background())
	models, _ := blockchain.Models(context.Background())
	if params.TokenPrice != 3 || models["scenario-model"].MaxModelLen != 4096 {
		t.Fatalf("EscrowParams().TokenPrice = %d, Models()[scenario-model].MaxModelLen = %d, want 3 and 4096", params.TokenPrice, models["scenario-model"].MaxModelLen)
	}
	blockchain.mu.Lock()
	result, created := blockchain.applyCreateLocked(creatorAddress, "scenario-model", 50_000)
	blockchain.mu.Unlock()
	if result.Code != chainCodeRejected || created != nil {
		t.Fatalf("applyCreateLocked() with creates rejected = code %d, created %v, want code %d and nothing created", result.Code, created, chainCodeRejected)
	}
	if wallet, _ := blockchain.SpendableBalance(context.Background(), creatorAddress, ""); wallet != 10_000_500 {
		t.Fatalf("SpendableBalance() = %d, want 10000500", wallet)
	}
	if _, found, _ := blockchain.Escrow(context.Background(), escrowID); found {
		t.Fatalf("Escrow(%d) after dropEscrow = found, want not found", escrowID)
	}
}

// Test flow:
//  1. Inside a bubble, make a chain at height 1000 advance one block every 5 s and sleep 12 s.
//  2. Assert the epoch reads height 1002.
//  3. Move the chain to height 1500, sleep 5 s, and assert it reads 1501: a scripted move re-anchors the clock.
func TestFakeChainHeightsAdvanceWithBubbleTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		blockchain := testFakeChain(inferenceEpoch())
		blockchain.advanceHeightsEvery(5 * time.Second)

		time.Sleep(12 * time.Second)
		if height := blockchain.snapshotEpoch().blockHeight; height != 1002 {
			t.Fatalf("snapshotEpoch().blockHeight after 12 s = %d, want 1002", height)
		}
		blockchain.moveToHeight(1500)
		time.Sleep(5 * time.Second)
		if height := blockchain.snapshotEpoch().blockHeight; height != 1501 {
			t.Fatalf("snapshotEpoch().blockHeight 5 s after moving to 1500 = %d, want 1501", height)
		}
	})
}
