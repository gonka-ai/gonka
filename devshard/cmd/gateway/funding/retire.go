package funding

import (
	"cmp"
	"slices"
	"time"
)

const (
	idleRetireAfter = 10 * time.Minute
	surplusSamples  = 3
)

func plannedRetires(state ModelState, routable []EscrowState, decision Decision) []Retire {
	retires := nonceCapRetires(state.Escrows)
	if !state.Priced || state.InBridgeWindow {
		return retires
	}
	if retire, planned := oneRetire(state, routable, decision); planned {
		retires = append(retires, retire)
	}
	return retires
}

func nonceCapRetires(escrows []EscrowState) []Retire {
	var retires []Retire
	for _, escrow := range escrows {
		if escrow.NonceCapReached {
			retires = append(retires, Retire{EscrowID: escrow.ID, Reason: ReasonNonceCap})
		}
	}
	slices.SortFunc(retires, func(left, right Retire) int { return cmp.Compare(left.EscrowID, right.EscrowID) })
	return retires
}

func oneRetire(state ModelState, routable []EscrowState, decision Decision) (Retire, bool) {
	starved := leastFreeFirst(routable, func(escrow EscrowState) bool { return escrow.Money.Starved && !escrow.Unread })
	if budgetPressed(state, decision) && len(starved) > 0 {
		return Retire{EscrowID: starved[0].ID, Reason: ReasonBudgetPressure}, true
	}
	for _, escrow := range starved {
		if escrow.Money.Idle && escrow.IdleFor >= idleRetireAfter {
			return Retire{EscrowID: escrow.ID, Reason: ReasonIdleStarved}, true
		}
	}
	if !decision.SurplusHeld || surplusSamplesHeld(state) < surplusSamples {
		return Retire{}, false
	}
	if len(starved) > 0 && decision.FullCount > state.FullContextSlots {
		return Retire{EscrowID: starved[0].ID, Reason: ReasonSurplus}, true
	}
	full := leastFreeFirst(routable, func(escrow EscrowState) bool { return escrow.Money.Full && !escrow.Unread })
	if len(full) > 0 && decision.FullCount >= state.FullContextSlots+2 {
		return Retire{EscrowID: full[0].ID, Reason: ReasonSurplus}, true
	}
	return Retire{}, false
}

func budgetDeficit(state ModelState, decision Decision) int {
	return regularWant(decision.Shortfalls) - decision.Room
}

func budgetPressed(state ModelState, decision Decision) bool {
	parking := max(state.Parking, 0)
	walletPressed := state.WalletShort && regularWant(decision.Shortfalls) > 0 && parking == 0
	return budgetDeficit(state, decision) > parking || walletPressed
}

func surplusSamplesHeld(state ModelState) int {
	if state.ScheduledTick {
		return state.SurplusStreak + 1
	}
	return state.SurplusStreak
}

func leastFreeFirst(escrows []EscrowState, keep func(EscrowState) bool) []EscrowState {
	kept := make([]EscrowState, 0, len(escrows))
	for _, escrow := range escrows {
		if keep(escrow) {
			kept = append(kept, escrow)
		}
	}
	slices.SortFunc(kept, func(left, right EscrowState) int {
		return cmp.Or(cmp.Compare(left.Money.Free, right.Money.Free), cmp.Compare(left.ID, right.ID))
	})
	return kept
}

func surplusHeld(state ModelState, shortfalls Shortfalls, routable []EscrowState, need uint64) bool {
	perEscrow := subtractFloor(state.Amount, state.FullCost)
	if !state.Priced || !state.DemandWindowFull || perEscrow == 0 || regularWant(shortfalls) > 0 {
		return false
	}
	var fullFree uint64
	for _, escrow := range routable {
		if escrow.Money.Full {
			fullFree = saturatingAdd(fullFree, atModelPrice(state, escrow, escrow.Money.Free))
		}
	}
	wanted := saturatingAdd(uint64(max(state.FullContextSlots, 0)), ceilDivide(subtractFloor(need, fullFree), perEscrow))
	return uint64(len(routable)) > max(uint64(max(state.TargetCount, 0)), wanted)
}
