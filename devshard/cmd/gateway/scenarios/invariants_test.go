package scenarios

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/app"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/funding"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/scheduler"
	"devshard/types"
)

const (
	timeoutBufferSeconds   = 5
	sweepGraceSeconds      = 120
	tempRole               = "temp"
	regularRole            = "regular"
	reservationReturnBound = (executionTimeoutSeconds+timeoutBufferSeconds+sweepGraceSeconds)*time.Second + 2*escrow.TickInterval
	requestBound           = (executionTimeoutSeconds + timeoutBufferSeconds) * time.Second
)

const recoveredReason = "recovered"

const (
	moneyIdentityKey            = "money identity"
	settlementMatchKey          = "settlement match"
	settleDeadlineKey           = "settle deadline"
	unsettledBudgetKey          = "unsettled budget"
	createReasonKey             = "create reason"
	activeEscrowRoutesKey       = "active escrow routes"
	inactiveEscrowUnroutableKey = "inactive escrow unroutable"
	requestEndsKey              = "request ends"
	ledgerMatchKey              = "ledger match"
	ledgerClassifiedKey         = "ledger classifies every nonce"
)

const ledgerSweepInterval = 10 * time.Second

type servingEscrow struct {
	escrowID string
	role     string
	balance  uint64
	label    int64
}

type plannedFigures struct {
	found      bool
	moneyShort bool
	need       uint64
	liquid     uint64
	fullCount  int
}

type createCandidate struct {
	createdID      string
	model          string
	role           string
	reason         string
	reasonKnown    bool
	serving        []servingEscrow
	fullCost       uint64
	guaranteeSlots int
	targetCount    int
	currentLabel   int64
	planned        plannedFigures
	samePlan       []string
	unreadFull     []string
}

type createLine struct {
	reason string
	role   string
	index  int
}

func (h *gatewayHarness) violations() []violationReport {
	if h.currentGateway() == nil {
		return nil
	}
	return slices.Concat(
		h.moneyIdentity(),
		h.settlementsMatchSessions(),
		h.deadlinesKept(),
		h.budgetKept(),
		h.takeCreateViolations(),
		h.activeEscrowsRoute(),
		h.rowsMatchRegistry(),
		h.requestsEnd(),
		h.ledgerMatchesChain(),
	)
}

func (h *gatewayHarness) moneyIdentity() []violationReport {
	var found []violationReport
	escrowIDs := h.fleet.builtEscrowIDs()
	slices.Sort(escrowIDs)
	for _, escrowID := range escrowIDs {
		machine, built := h.fleet.userMachine(escrowID)
		record, known := h.chain.escrowRecord(parseEscrowID(escrowID))
		if !built || !known {
			continue
		}
		if detail := moneyIdentityViolation(escrowID, record.amount, machine.SnapshotState()); detail != "" {
			found = append(found, violationReport{key: moneyIdentityKey, detail: detail})
		}
	}
	return found
}

func moneyIdentityViolation(escrowID string, amount uint64, snapshot types.EscrowState) string {
	var costs, reserved uint64
	for _, stats := range snapshot.HostStats {
		costs += stats.Cost
	}
	for _, inference := range snapshot.Inferences {
		if inference.Status == types.StatusPending || inference.Status == types.StatusStarted {
			reserved += inference.ReservedCost
		}
	}
	total := snapshot.Balance + snapshot.Fees + costs + reserved
	if total == amount {
		return ""
	}
	return fmt.Sprintf("escrow %s: balance %d + fees %d + costs %d + reserved %d = %d, want amount %d",
		escrowID, snapshot.Balance, snapshot.Fees, costs, reserved, total, amount)
}

func (h *gatewayHarness) settlementsMatchSessions() []violationReport {
	var found []violationReport
	for _, model := range h.spec.allModels() {
		for _, record := range h.chain.escrowsOf(model.id) {
			escrowID := formatEscrowID(record.id)
			if record.pruned && !record.settled && !h.spec.allowUnsettled {
				found = append(found, violationReport{key: settlementMatchKey, detail: fmt.Sprintf("escrow %s was pruned with its whole amount %d", escrowID, record.amount)})
			}
			if record.settled && record.refund != record.amount-record.settledCosts-record.settledFees {
				found = append(found, violationReport{key: settlementMatchKey, detail: fmt.Sprintf("escrow %s refunded %d, want amount %d less costs %d and fees %d: the settle left fees unpaid to its slots",
					escrowID, record.refund, record.amount, record.settledCosts, record.settledFees)})
			}
			machine, built := h.fleet.userMachine(escrowID)
			if !record.settled || !built {
				continue
			}
			snapshot := machine.SnapshotState()
			var costs uint64
			for _, stats := range snapshot.HostStats {
				costs += stats.Cost
			}
			if costs != record.settledCosts || snapshot.Fees != record.settledFees {
				found = append(found, violationReport{key: settlementMatchKey, detail: fmt.Sprintf("escrow %s settled costs %d fees %d, session holds costs %d fees %d",
					escrowID, record.settledCosts, record.settledFees, costs, snapshot.Fees)})
			}
		}
	}
	return found
}

func (h *gatewayHarness) deadlinesKept() []violationReport {
	if h.spec.allowUnsettled {
		return nil
	}
	effective := h.chain.snapshotEpoch().effective
	var found []violationReport
	for _, model := range h.spec.allModels() {
		for _, record := range h.chain.escrowsOf(model.id) {
			if !record.settled && effective >= record.epochIndex+2 {
				found = append(found, violationReport{key: settleDeadlineKey, detail: fmt.Sprintf("escrow %d of epoch %d unsettled at effective epoch %d", record.id, record.epochIndex, effective)})
			}
		}
	}
	return found
}

func (h *gatewayHarness) budgetKept() []violationReport {
	var found []violationReport
	for _, model := range h.spec.allModels() {
		unsettled := 0
		for _, record := range h.chain.escrowsOf(model.id) {
			if !record.settled && !record.pruned {
				unsettled++
			}
		}
		limit := h.spec.maxUnsettled
		switch {
		case model.maxUnsettled > 0:
			limit = min(limit, model.maxUnsettled)
		default:
			limit = min(limit, model.targetCount+model.tempCount+model.reserveCount+4)
		}
		if unsettled > limit {
			found = append(found, violationReport{key: unsettledBudgetKey, detail: fmt.Sprintf("model %s: %d unsettled escrows, want at most %d", model.id, unsettled, limit)})
		}
	}
	return found
}

// observeCreate runs inside the broadcast, before the row of the created escrow exists, so it only records what the create saw.
func (h *gatewayHarness) observeCreate(created chainCreate) {
	running := h.currentGateway()
	if running == nil {
		return
	}
	rows, err := running.Store().ListDevshards(context.Background())
	if err != nil {
		return
	}
	snapshot, configuration := running.Observer().Snapshot(), running.Config().Load()
	model := h.spec.modelByID(created.model)
	fullCost, _ := scheduler.RequestCost(configuration.Limits.RetirementReserve(model.id, snapshot.Models[model.id].MaxModelLen), snapshot.TokenPrice, snapshot.FeePerNonce)
	candidate := createCandidate{
		createdID: formatEscrowID(created.escrowID), model: created.model,
		fullCost:       fullCost * uint64(scheduler.AttemptsToFund(configuration.Engine.MaxAttemptsPerRequest)),
		guaranteeSlots: model.guaranteeSlots(), targetCount: model.targetCount, currentLabel: int64(snapshot.EpochIndex),
	}
	for _, row := range rows {
		if !row.Active || row.Model != created.model {
			continue
		}
		candidate.serving = append(candidate.serving, servingEscrow{
			escrowID: row.EscrowID, role: row.RotationRole, balance: h.userBalance(row.EscrowID), label: row.RotationEpoch,
		})
		if _, published := running.Escrows().Routable(row.EscrowID); !published && h.unreadBalance(row.EscrowID) >= candidate.fullCost {
			candidate.unreadFull = append(candidate.unreadFull, row.EscrowID)
		}
	}
	h.observationsMu.Lock()
	defer h.observationsMu.Unlock()
	h.unreadFullAt[candidate.createdID] = candidate.unreadFull
	h.createCandidates = append(h.createCandidates, candidate)
}

// unreadBalance is what an escrow with no live session holds: its session's balance if one was ever opened, else the amount the chain funded it with.
func (h *gatewayHarness) unreadBalance(escrowID string) uint64 {
	if _, built := h.fleet.userMachine(escrowID); built {
		return h.userBalance(escrowID)
	}
	record, _ := h.chain.escrowRecord(parseEscrowID(escrowID))
	return record.amount
}

func createLinesOf(entries []logcapture.Entry) map[string]createLine {
	lines := map[string]createLine{}
	for index, entry := range entries {
		escrowID, _ := logcapture.Field(entry, logkey.Escrow).(string)
		role, _ := logcapture.Field(entry, logkey.Role).(string)
		switch entry.Msg {
		case "escrow created":
			reason, _ := logcapture.Field(entry, logkey.Reason).(string)
			lines[escrowID] = createLine{reason: reason, role: role, index: index}
		case "escrow recovered from commitment":
			lines[escrowID] = createLine{reason: recoveredReason, role: role, index: index}
		}
	}
	return lines
}

func plannedBefore(entries []logcapture.Entry, model string, index int) plannedFigures {
	for position := index - 1; position >= 0; position-- {
		entry := entries[position]
		if entry.Msg != "escrow funding planned" || logcapture.Field(entry, logkey.Model) != model {
			continue
		}
		moneyShort, _ := logcapture.Field(entry, logkey.MoneyShort).(bool)
		need, _ := logcapture.Field(entry, logkey.Need).(uint64)
		liquid, _ := logcapture.Field(entry, logkey.Liquid).(uint64)
		fullCount, _ := logcapture.Field(entry, logkey.FullCount).(int)
		return plannedFigures{found: true, moneyShort: moneyShort, need: need, liquid: liquid, fullCount: fullCount}
	}
	return plannedFigures{}
}

// samePlanCreates are the escrows the plan narrated before the create at index had already created.
func samePlanCreates(entries []logcapture.Entry, model string, index int) []string {
	var created []string
	for position := index - 1; position >= 0; position-- {
		entry := entries[position]
		if logcapture.Field(entry, logkey.Model) != model {
			continue
		}
		if entry.Msg == "escrow funding planned" {
			return created
		}
		if escrowID, _ := logcapture.Field(entry, logkey.Escrow).(string); entry.Msg == "escrow created" {
			created = append(created, escrowID)
		}
	}
	return created
}

// certainlyUnread are the escrows a guard create's plan could not read though they held a full slot, less the plan's own creates.
func certainlyUnread(candidate createCandidate) []string {
	var unread []string
	for _, escrowID := range candidate.unreadFull {
		if !slices.Contains(candidate.samePlan, escrowID) && !slices.Contains(unread, escrowID) {
			unread = append(unread, escrowID)
		}
	}
	return unread
}

func createViolation(candidate createCandidate) string {
	switch candidate.reason {
	case "bridge", "standby", "operator":
		return ""
	case string(funding.ReasonGuard):
		switch {
		case !candidate.planned.found:
			return "a guard create with no funding plan narrated before it"
		case candidate.planned.fullCount >= candidate.guaranteeSlots:
			return fmt.Sprintf("a guard create planned while %d escrows were full, want fewer than %d", candidate.planned.fullCount, candidate.guaranteeSlots)
		}
		if unread := certainlyUnread(candidate); len(unread) > 0 && len(unread)+len(candidate.samePlan) >= candidate.guaranteeSlots {
			return fmt.Sprintf("a guard create while %v held a full slot unread and the same plan had created %v, want fewer than %d certainly full", unread, candidate.samePlan, candidate.guaranteeSlots)
		}
		return ""
	case string(funding.ReasonCapacity):
		switch {
		case !candidate.planned.found:
			return "a capacity create with no funding plan narrated before it"
		case !candidate.planned.moneyShort && candidate.planned.need <= candidate.planned.liquid:
			return fmt.Sprintf("a capacity create with need %d within liquid %d and no money-short signal", candidate.planned.need, candidate.planned.liquid)
		}
		return ""
	case string(funding.ReasonSpread):
		if current := currentRegulars(candidate); current >= candidate.targetCount {
			return fmt.Sprintf("a spread create while %d regulars of label %d served, target %d", current, candidate.currentLabel, candidate.targetCount)
		}
		return ""
	}
	return legacyCreateViolation(candidate)
}

func currentRegulars(candidate createCandidate) int {
	current := 0
	for _, serving := range candidate.serving {
		if serving.role != tempRole && serving.role != escrow.RoleReserve && serving.label == candidate.currentLabel {
			current++
		}
	}
	return current
}

func legacyCreateViolation(candidate createCandidate) string {
	if candidate.role == tempRole || candidate.role == escrow.RoleReserve {
		return ""
	}
	for _, serving := range candidate.serving {
		if serving.role != escrow.RoleReserve && serving.balance >= candidate.fullCost {
			return fmt.Sprintf("a %s escrow was created while %s held %d, at least %d", candidate.role, serving.escrowID, serving.balance, candidate.fullCost)
		}
	}
	return ""
}

func judgeCreates(candidates []createCandidate, runEnded bool) (found []violationReport, pending []createCandidate) {
	for _, candidate := range candidates {
		if candidate.role == "" || !candidate.reasonKnown {
			if !runEnded {
				pending = append(pending, candidate)
				continue
			}
			found = append(found, violationReport{key: createReasonKey, detail: fmt.Sprintf("escrow %s was created but never stored with a role or narrated with a reason (role %q)", candidate.createdID, candidate.role)})
			continue
		}
		if detail := createViolation(candidate); detail != "" {
			found = append(found, violationReport{key: createReasonKey, detail: "escrow " + candidate.createdID + ": " + detail})
		}
	}
	return found, pending
}

func (h *gatewayHarness) takeCreateViolations() []violationReport {
	if !h.resolveCandidates() {
		return nil
	}
	h.observationsMu.Lock()
	defer h.observationsMu.Unlock()
	return h.judgeCreatesLocked(false)
}

func (h *gatewayHarness) resolveCandidates() bool {
	running := h.currentGateway()
	if running == nil {
		return false
	}
	rows, err := running.Store().ListDevshards(context.Background())
	if err != nil {
		return false
	}
	rolesByID := make(map[string]string, len(rows))
	for _, row := range rows {
		rolesByID[row.EscrowID] = row.RotationRole
	}
	entries := h.loggedEntries()
	lines := createLinesOf(entries)
	h.observationsMu.Lock()
	defer h.observationsMu.Unlock()
	for index := range h.createCandidates {
		candidate := &h.createCandidates[index]
		if role, known := createdRole(candidate.createdID, rolesByID, lines); known && candidate.role == "" {
			candidate.role = role
		}
		if line, logged := lines[candidate.createdID]; logged && !candidate.reasonKnown {
			candidate.reason, candidate.reasonKnown = line.reason, true
			switch funding.Reason(line.reason) {
			case funding.ReasonCapacity, funding.ReasonGuard:
				candidate.planned = plannedBefore(entries, candidate.model, line.index)
				candidate.samePlan = samePlanCreates(entries, candidate.model, line.index)
				if len(candidate.samePlan) > 0 {
					candidate.unreadFull = append(candidate.unreadFull, h.unreadFullAt[candidate.samePlan[len(candidate.samePlan)-1]]...)
				}
			}
		}
	}
	return true
}

// createdRole reads a create's role from its stored row, or from its create line once settlement has dropped the row.
func createdRole(createdID string, rolesByID map[string]string, lines map[string]createLine) (string, bool) {
	if role, stored := rolesByID[createdID]; stored {
		return role, true
	}
	if line, logged := lines[createdID]; logged && line.role != "" {
		return line.role, true
	}
	return "", false
}

// takeDeferredCreateViolations judges, once the gateway has stopped and every tick has unwound, the creates still waiting for their role or reason.
func (h *gatewayHarness) takeDeferredCreateViolations() []violationReport {
	h.observationsMu.Lock()
	defer h.observationsMu.Unlock()
	return h.judgeCreatesLocked(true)
}

func (h *gatewayHarness) judgeCreatesLocked(runEnded bool) []violationReport {
	found, pending := judgeCreates(h.createCandidates, runEnded)
	h.createCandidates = pending
	return found
}

func (h *gatewayHarness) activeEscrowsRoute() []violationReport {
	running := h.currentGateway()
	if running == nil {
		return nil
	}
	var found []violationReport
	for _, row := range h.rows() {
		if !row.Active || row.SettlementPending {
			continue
		}
		if _, routable := running.Escrows().Routable(row.EscrowID); !routable {
			found = append(found, violationReport{key: activeEscrowRoutesKey, detail: fmt.Sprintf("escrow %s active but absent from routing", row.EscrowID)})
		}
	}
	return found
}

func (h *gatewayHarness) rowsMatchRegistry() []violationReport {
	running := h.currentGateway()
	if running == nil {
		return nil
	}
	var found []violationReport
	for _, row := range h.rows() {
		if _, routable := running.Escrows().Routable(row.EscrowID); !row.Active && routable {
			found = append(found, violationReport{key: inactiveEscrowUnroutableKey, detail: fmt.Sprintf("escrow %s inactive in the store but routable", row.EscrowID)})
		}
	}
	return found
}

func (h *gatewayHarness) requestsEnd() []violationReport {
	now := time.Now()
	h.requestsMu.Lock()
	defer h.requestsMu.Unlock()
	var found []violationReport
	for requestID, startedAt := range h.openRequests {
		if now.Sub(startedAt) > requestBound {
			found = append(found, violationReport{key: requestEndsKey, detail: fmt.Sprintf("request %d open for %s, want at most %s", requestID, now.Sub(startedAt), requestBound)})
		}
	}
	return found
}

// judge tolerates a violation only when the scenario pins its key; anything else stops the scenario.
func (h *gatewayHarness) judge(where string, found []violationReport) {
	for _, violation := range found {
		note, pinned := h.spec.pins[violation.key]
		if !pinned {
			if h.spec.violationSink != nil && *h.spec.violationSink == "" {
				*h.spec.violationSink = violation.key
			}
			h.t.Fatalf("%s: %s: %s", where, violation.key, violation.detail)
		}
		if !h.pinsSeen[violation.key] {
			h.t.Logf("%s: pinned bug %s (%s) reproduced: %s", where, violation.key, note, violation.detail)
		}
		h.pinsSeen[violation.key] = true
	}
}

func (h *gatewayHarness) requirePinsSeen() {
	for _, key := range unseenPins(h.spec.pins, h.pinsSeen) {
		h.t.Fatalf("pinned bug %s (%s) no longer reproduces: remove the pin and keep the scenario as a regression test", key, h.spec.pins[key])
	}
}

func unseenPins(pins map[string]string, seen map[string]bool) []string {
	var unseen []string
	for key := range pins {
		if !seen[key] {
			unseen = append(unseen, key)
		}
	}
	slices.Sort(unseen)
	return unseen
}

// Test flow:
//  1. Build pins for the active escrow routes invariant and one expectation label, with only the invariant seen.
//  2. Assert unseenPins reports exactly the expectation label.
func TestUnseenPinsListsOnlyThePinsThatNeverFired(t *testing.T) {
	unseen := unseenPins(map[string]string{activeEscrowRoutesKey: "hold", "a pinned expectation": "fan-out"}, map[string]bool{activeEscrowRoutesKey: true})
	if len(unseen) != 1 || unseen[0] != "a pinned expectation" {
		t.Fatalf("unseenPins(pins, seen) = %v, want [a pinned expectation]", unseen)
	}
}

// Test flow:
//  1. Build a session state from balance, fees, host costs and inferences.
//  2. Assert moneyIdentityViolation reports a violation exactly when the parts do not add up to the amount.
func TestMoneyIdentityViolation(t *testing.T) {
	build := func(balance, fees, cost uint64, inferences map[uint64]*types.InferenceRecord) types.EscrowState {
		return types.EscrowState{Balance: balance, Fees: fees, HostStats: map[uint32]*types.HostStats{0: {Cost: cost}}, Inferences: inferences}
	}
	testCases := []struct {
		name      string
		state     types.EscrowState
		violation bool
	}{
		{"consistent state", build(900, 50, 50, nil), false},
		{"balance off by one", build(901, 50, 50, nil), true},
		{"pending reserve counted", build(800, 50, 50, map[uint64]*types.InferenceRecord{1: {Status: types.StatusPending, ReservedCost: 100}}), false},
		{"started reserve counted", build(800, 50, 50, map[uint64]*types.InferenceRecord{1: {Status: types.StatusStarted, ReservedCost: 100}}), false},
		{"finished reserve not counted", build(800, 50, 50, map[uint64]*types.InferenceRecord{1: {Status: types.StatusFinished, ReservedCost: 100}}), true},
		{"challenged actual cost counted only through host stats", build(900, 50, 50, map[uint64]*types.InferenceRecord{1: {Status: types.StatusChallenged, ReservedCost: 100, ActualCost: 40}}), false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			detail := moneyIdentityViolation("1", 1000, testCase.state)

			if (detail != "") != testCase.violation {
				t.Fatalf("moneyIdentityViolation(%s) = %q, want violation %v", testCase.name, detail, testCase.violation)
			}
		})
	}
}

// Test flow:
//  1. Ask retentionMayPrune about escrows created before, at and after the retention edge, with retention off, and before the current epoch passes the retention.
//  2. Assert it allows a prune only for an escrow created more than the retention before the current epoch, and never with retention off.
func TestRetentionMayPrune(t *testing.T) {
	testCases := []struct {
		name            string
		creationEpoch   uint64
		currentEpoch    uint64
		retentionEpochs int64
		mayPrune        bool
	}{
		{"created before the retention edge", 7, 10, 2, true},
		{"created at the retention edge", 8, 10, 2, false},
		{"created in the current epoch", 10, 10, 2, false},
		{"retention off", 1, 10, 0, false},
		{"current epoch within the retention", 0, 2, 2, false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			mayPrune := retentionMayPrune(testCase.creationEpoch, testCase.currentEpoch, testCase.retentionEpochs)

			if mayPrune != testCase.mayPrune {
				t.Fatalf("retentionMayPrune(%d, %d, %d) = %v, want %v", testCase.creationEpoch, testCase.currentEpoch, testCase.retentionEpochs, mayPrune, testCase.mayPrune)
			}
		})
	}
}

func (h *gatewayHarness) ledgerMatchesChain() []violationReport {
	running := h.currentGateway()
	if running == nil || running.Nonces() == nil {
		return nil
	}
	book := running.Nonces().Book()
	currentEpoch, retentionEpochs := running.Observer().Snapshot().EpochIndex, running.Config().Load().NonceAccounting.RetentionEpochs
	var found []violationReport
	for _, model := range h.spec.allModels() {
		for _, record := range h.chain.escrowsOf(model.id) {
			if !record.settled {
				continue
			}
			totals, known := book.MoneyTotals(formatEscrowID(record.id))
			switch {
			case !known && record.settledCosts > 0 && !retentionMayPrune(record.epochIndex, currentEpoch, retentionEpochs):
				found = append(found, violationReport{key: ledgerMatchKey, detail: fmt.Sprintf("escrow %d settled with costs %d, but the ledger never opened it", record.id, record.settledCosts)})
			case known:
				h.ledgerComparisons++
				if totals.Charged != record.settledCosts {
					found = append(found, violationReport{key: ledgerMatchKey, detail: fmt.Sprintf("escrow %d settled costs %d, ledger charged %d", record.id, record.settledCosts, totals.Charged)})
				}
			}
		}
	}
	return found
}

// retentionMayPrune reports whether the ledger's retention may already have dropped a retired escrow of creationEpoch.
func retentionMayPrune(creationEpoch, currentEpoch uint64, retentionEpochs int64) bool {
	return retentionEpochs > 0 && currentEpoch > uint64(retentionEpochs) && creationEpoch < currentEpoch-uint64(retentionEpochs)
}

// ledgerAtRest stops creating and retiring, then compares the ledger with every live session after each sweep interval, and reports a mismatch once no session's money moved for a whole interval and no nonce waits to be classified, or reservationReturnBound has passed.
func (h *gatewayHarness) ledgerAtRest() []violationReport {
	running := h.currentGateway()
	if running == nil || running.Nonces() == nil {
		return nil
	}
	running.Manager().Stop()
	deadline := time.Now().Add(reservationReturnBound)
	for {
		moved := sessionsMovedWithin(running, ledgerSweepInterval+time.Second)
		synctest.Wait()
		found := h.ledgerMismatches(running)
		if len(found) == 0 || (!moved && !awaitsClassification(found)) || !time.Now().Before(deadline) {
			return found
		}
	}
}

// sessionsMovedWithin samples every live session's reserved and challenged money once a second through window.
func sessionsMovedWithin(running *app.Composed, window time.Duration) bool {
	before, moved := sessionFunds(running), false
	for range int(window / time.Second) {
		time.Sleep(time.Second)
		if current := sessionFunds(running); !maps.Equal(current, before) {
			before, moved = current, true
		}
	}
	return moved
}

func sessionFunds(running *app.Composed) map[string][2]uint64 {
	funds := map[string][2]uint64{}
	for _, state := range running.Escrows().Snapshot() {
		if reserved, challenged, live := sessionMoney(running, state.ID); live {
			funds[state.ID] = [2]uint64{reserved, challenged}
		}
	}
	return funds
}

// sessionMoney is what a live escrow's session holds for unresolved nonces: Pending and Started reservations, and Challenged costs.
func sessionMoney(running *app.Composed, escrowID string) (reserved, challenged uint64, live bool) {
	session, known := running.Escrows().RoutableSession(escrowID)
	if !known {
		return 0, 0, false
	}
	for _, record := range session.SnapshotState().Inferences {
		switch record.Status {
		case types.StatusPending, types.StatusStarted:
			reserved += record.ReservedCost
		case types.StatusChallenged:
			challenged += record.ActualCost
		}
	}
	return reserved, challenged, true
}

func (h *gatewayHarness) ledgerMismatches(running *app.Composed) []violationReport {
	book := running.Nonces().Book()
	var found []violationReport
	for _, state := range running.Escrows().Snapshot() {
		reserved, challenged, live := sessionMoney(running, state.ID)
		totals, known := book.MoneyTotals(state.ID)
		switch {
		case !live:
			continue
		case !known && h.fleet.inferencesStartedOn(state.ID) > 0:
			found = append(found, violationReport{key: ledgerMatchKey, detail: fmt.Sprintf("escrow %s served traffic, but the ledger never opened it", state.ID)})
			continue
		case !known:
			continue
		}
		h.ledgerComparisons++
		if totals.InFlightReserved != reserved || totals.Challenged != challenged {
			found = append(found, violationReport{key: ledgerMatchKey, detail: fmt.Sprintf("escrow %s ledger reserved %d challenged %d, session reserved %d challenged %d", state.ID, totals.InFlightReserved, totals.Challenged, reserved, challenged)})
		}
		found = append(found, ledgerClassificationGaps(book, state.ID, running.Nonces().UnknownEscrowFacts())...)
	}
	return found
}

func awaitsClassification(found []violationReport) bool {
	return slices.ContainsFunc(found, func(violation violationReport) bool { return violation.key == ledgerClassifiedKey })
}

func ledgerClassificationGaps(book *accounting.Book, escrowID string, unknownEscrowFacts uint64) []violationReport {
	var unclassified, overclassified uint64
	for _, record := range book.Query(accounting.QueryFilter{EscrowIDs: []string{escrowID}}) {
		unclassified += record.Unobserved
		overclassified += record.Overcounted
	}
	var found []violationReport
	if overclassified > 0 {
		found = append(found, violationReport{key: ledgerClassifiedKey, detail: fmt.Sprintf("escrow %s ledger classified %d nonces more than the chain assigned; %d facts dropped for escrows the ledger did not hold", escrowID, overclassified, unknownEscrowFacts)})
	}
	if unclassified > 0 {
		found = append(found, violationReport{key: ledgerClassifiedKey, detail: fmt.Sprintf("escrow %s ledger left %d assigned nonces unclassified; %d facts dropped for escrows the ledger did not hold", escrowID, unclassified, unknownEscrowFacts)})
	}
	return found
}
