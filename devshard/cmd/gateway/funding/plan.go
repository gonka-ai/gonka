// Package funding decides, for one model, which escrows to create and which to retire. See README.md.
package funding

import (
	"slices"
	"time"

	"devshard/cmd/gateway/liquidity"
)

const demandPeakPerHeadroom = 2

// EscrowState is one live escrow of the model as the planner sees it. See README.md, "Inputs".
type EscrowState struct {
	ID              string
	Standby         bool
	Temp            bool
	Unread          bool
	Label           int64
	NonceCapReached bool
	FullCost        uint64
	IdleFor         time.Duration
	Money           liquidity.Escrow
}

// ModelState is everything Plan reads for one model. See README.md, "Inputs".
type ModelState struct {
	Escrows               []EscrowState
	Counted               int
	TargetCount           int
	TempCount             int
	ReserveCount          int
	MaxUnsettled          int
	FullContextSlots      int
	Amount                uint64
	CreateFee             uint64
	FullCost              uint64
	Slot                  uint64
	Priced                bool
	DemandPeak            uint64
	DemandWindowFull      bool
	SurplusStreak         int
	ScheduledTick         bool
	Parking               int
	PendingRegularCreates int
	PendingStandbyCreates int
	MoneyShort            bool
	WalletShort           bool
	RequestsBlocked       bool
	EpochKnown            bool
	ServedByNetwork       bool
	BreakerGated          bool
	InBridgeWindow        bool
	NearBridgeWindow      bool
	CurrentLabel          int64
	BucketTokens          int
}

// Shortfalls are the four terms a model's creates are sized from. See README.md, "The algorithm".
type Shortfalls struct {
	Guard    int
	Capacity int
	Spread   int
	Standby  int
}

// Create is one escrow the planner wants funded.
type Create struct {
	Standby bool
	Reason  Reason
}

// Retire is one escrow the planner wants parked.
type Retire struct {
	EscrowID string
	Reason   Reason
}

// Decision is what Plan wants for one model in one tick. See README.md, "The algorithm".
type Decision struct {
	Creates       []Create
	Retires       []Retire
	Shortfalls    Shortfalls
	FullCount     int
	Room          int
	AmountNeeded  uint64
	Need          uint64
	Liquid        uint64
	Broken        bool
	Misconfigured bool
	BudgetReached bool
	SurplusHeld   bool
}

// Plan sizes one model's creates and its planned retires from its money, demand and budget. See README.md, "The algorithm".
func Plan(state ModelState) Decision {
	routable := routableEscrows(state.Escrows)
	decision := Decision{FullCount: fullCount(state.Escrows), Misconfigured: misconfigured(state), AmountNeeded: amountNeeded(state)}
	decision.Need, decision.Liquid = demandNeed(state, decision.Misconfigured), liquidMoney(state, routable)
	decision.Shortfalls = Shortfalls{
		Guard:    guardShortfall(state, decision),
		Capacity: capacityShortfall(state, decision.Need, decision.Liquid),
		Spread:   spreadShortfall(state, routable),
		Standby:  standbyShortfall(state),
	}
	decision.Room = state.MaxUnsettled - state.Counted - bridgeReservation(state)
	regularWanted := max(0, regularWant(decision.Shortfalls)-state.PendingRegularCreates)
	wanted := regularWanted + max(0, decision.Shortfalls.Standby-state.PendingStandbyCreates)
	decision.BudgetReached = wanted > decision.Room
	count := min(wanted, max(decision.Room, 0), max(state.BucketTokens, 0))
	decision.Creates = plannedCreates(state, decision.Shortfalls, regularWanted, count)
	decision.Broken = regularCreates(decision.Creates)+state.PendingRegularCreates < decision.Shortfalls.Guard || (state.WalletShort && decision.Shortfalls.Guard > 0)
	decision.SurplusHeld = surplusHeld(state, decision.Shortfalls, routable, decision.Need)
	decision.Retires = plannedRetires(state, routable, decision)
	return decision
}

// Reasons are the decision's create and retire reasons, each once, in the order they first appear.
func (decision Decision) Reasons() []Reason {
	var reasons []Reason
	add := func(reason Reason) {
		if !slices.Contains(reasons, reason) {
			reasons = append(reasons, reason)
		}
	}
	for _, create := range decision.Creates {
		add(create.Reason)
	}
	for _, retire := range decision.Retires {
		add(retire.Reason)
	}
	return reasons
}

func routableEscrows(escrows []EscrowState) []EscrowState {
	routable := make([]EscrowState, 0, len(escrows))
	for _, escrow := range escrows {
		if !escrow.Standby && !escrow.NonceCapReached {
			routable = append(routable, escrow)
		}
	}
	return routable
}

func fullCount(escrows []EscrowState) int {
	count := 0
	for _, escrow := range escrows {
		if escrow.Money.Full && !escrow.NonceCapReached {
			count++
		}
	}
	return count
}

func misconfigured(state ModelState) bool {
	return state.Priced && (state.Amount <= state.CreateFee || state.Amount-state.CreateFee < state.Slot)
}

func amountNeeded(state ModelState) uint64 {
	return saturatingAdd(state.CreateFee, state.Slot)
}

func demandNeed(state ModelState, misconfigured bool) uint64 {
	headroom := saturatingAdd(state.DemandPeak, state.DemandPeak/demandPeakPerHeadroom)
	if misconfigured {
		return headroom
	}
	return saturatingAdd(headroom, saturatingMul(uint64(max(state.FullContextSlots, 0)), state.Slot))
}

func liquidMoney(state ModelState, routable []EscrowState) uint64 {
	var liquid uint64
	for _, escrow := range routable {
		ownPrice := saturatingAdd(subtractFloor(escrow.Money.Free, escrow.FullCost), escrow.Money.Returning)
		liquid = saturatingAdd(liquid, atModelPrice(state, escrow, ownPrice))
	}
	return liquid
}

// atModelPrice is what money an escrow spends at its own session price buys at the model's current price. See README.md, "Money at one price".
func atModelPrice(state ModelState, escrow EscrowState, amount uint64) uint64 {
	if escrow.FullCost == 0 || state.FullCost == 0 {
		return amount
	}
	return scaledBy(amount, state.FullCost, escrow.FullCost)
}

func guardShortfall(state ModelState, decision Decision) int {
	if !state.Priced || decision.Misconfigured {
		return 0
	}
	return max(0, state.FullContextSlots-decision.FullCount)
}

func capacityShortfall(state ModelState, need, liquid uint64) int {
	if !state.Priced || (!state.MoneyShort && need <= liquid) {
		return 0
	}
	perEscrow := subtractFloor(subtractFloor(state.Amount, state.CreateFee), state.FullCost)
	if perEscrow == 0 {
		return 0
	}
	return boundedCount(ceilDivide(max(1, subtractFloor(need, liquid)), perEscrow), state)
}

func spreadShortfall(state ModelState, routable []EscrowState) int {
	current := 0
	for _, escrow := range routable {
		if !escrow.Temp && escrow.Label == state.CurrentLabel {
			current++
		}
	}
	return max(0, state.TargetCount-current)
}

func standbyShortfall(state ModelState) int {
	current := 0
	for _, escrow := range state.Escrows {
		if escrow.Standby && !escrow.NonceCapReached && escrow.Label >= state.CurrentLabel {
			current++
		}
	}
	return max(0, state.ReserveCount-current)
}

func bridgeReservation(state ModelState) int {
	if state.NearBridgeWindow && !state.InBridgeWindow {
		return state.TempCount
	}
	return 0
}

func boundedCount(count uint64, state ModelState) int {
	return int(min(count, uint64(max(state.MaxUnsettled, 0))+1))
}

func regularWant(shortfalls Shortfalls) int {
	return max(shortfalls.Guard, shortfalls.Capacity, shortfalls.Spread)
}

func plannedCreates(state ModelState, shortfalls Shortfalls, regularWanted, count int) []Create {
	if count <= 0 || !createsAllowed(state) {
		return nil
	}
	creates := make([]Create, 0, count)
	for index := range min(count, regularWanted) {
		creates = append(creates, Create{Reason: regularReason(index+state.PendingRegularCreates, shortfalls)})
	}
	for len(creates) < count {
		creates = append(creates, Create{Standby: true, Reason: ReasonStandby})
	}
	return creates
}

func createsAllowed(state ModelState) bool {
	return !state.RequestsBlocked && state.EpochKnown && state.ServedByNetwork && !state.BreakerGated && !state.InBridgeWindow
}

func regularReason(index int, shortfalls Shortfalls) Reason {
	switch {
	case index < shortfalls.Guard:
		return ReasonGuard
	case index < shortfalls.Capacity:
		return ReasonCapacity
	}
	return ReasonSpread
}

func regularCreates(creates []Create) int {
	count := 0
	for _, create := range creates {
		if !create.Standby {
			count++
		}
	}
	return count
}
