package escrow

import (
	"context"
	"slices"
	"strings"

	"devshard/cmd/gateway/store"
)

const chainFactsPerTick = 16

func (m *Manager) resolveChainFacts(ctx context.Context, devshards []store.DevshardRecord) []store.DevshardRecord {
	if m.chainFacts == nil {
		return devshards
	}
	resolved := slices.Clone(devshards)
	unresolved := make([]int, 0, len(resolved))
	unresolvedIDs := make(map[string]bool)
	for index, record := range resolved {
		if !goneFromChain(record) && (record.ChainEpoch == 0 || record.Amount == 0) {
			unresolved = append(unresolved, index)
			unresolvedIDs[record.EscrowID] = true
		}
	}
	m.unresolvedNarrated.retain(func(escrowID string) bool { return unresolvedIDs[escrowID] })
	if len(unresolved) == 0 {
		return resolved
	}
	escrowIDAt := func(index int) string { return resolved[index].EscrowID }
	for _, index := range nextInTurn(unresolved, escrowIDAt, m.chainFactsAfter, chainFactsPerTick) {
		m.chainFactsAfter = escrowIDAt(index)
		if chainEpoch, amount, read := m.readChainFacts(ctx, escrowIDAt(index)); read {
			resolved[index].ChainEpoch, resolved[index].Amount = chainEpoch, amount
		}
	}
	return resolved
}

// nextInTurn takes up to budget candidates in escrow-id order after the one the previous walk stopped at, wrapping round, so a candidate that never clears cannot starve the rest.
func nextInTurn(candidates []int, escrowIDAt func(int) string, after string, budget int) []int {
	slices.SortFunc(candidates, func(left, right int) int { return strings.Compare(escrowIDAt(left), escrowIDAt(right)) })
	start, _ := slices.BinarySearchFunc(candidates, after, func(index int, after string) int {
		if escrowIDAt(index) <= after {
			return -1
		}
		return 1
	})
	turn := make([]int, 0, min(budget, len(candidates)))
	for offset := range min(budget, len(candidates)) {
		turn = append(turn, candidates[(start+offset)%len(candidates)])
	}
	return turn
}

func (m *Manager) readChainFacts(ctx context.Context, escrowID string) (chainEpoch, amount uint64, read bool) {
	if m.chainFacts == nil {
		return 0, 0, false
	}
	info, found, err := m.chainFacts.GetEscrow(ctx, escrowID)
	switch {
	case err != nil:
		m.narrateUnresolved(escrowID, chainFactsLookupFailed)
		return 0, 0, false
	case !found:
		m.narrateUnresolved(escrowID, chainFactsNotOnChain)
		return 0, 0, false
	case info.EpochIndex == 0:
		m.narrateUnresolved(escrowID, chainFactsNoEpoch)
		return 0, 0, false
	}
	if err := m.store.WithRetry(ctx, func() error {
		return m.store.SetDevshardChainFacts(ctx, escrowID, info.EpochIndex, info.Balance)
	}); err != nil {
		m.narrateUnresolved(escrowID, chainFactsWriteFailed)
		return 0, 0, false
	}
	m.unresolvedNarrated.forget(escrowID)
	return info.EpochIndex, info.Balance, true
}

func (m *Manager) narrateUnresolved(escrowID string, reason chainFactsFailure) {
	if !m.unresolvedNarrated.mark(escrowID) || m.narrator == nil {
		return
	}
	m.narrator.EscrowChainFactsUnresolved(escrowID, string(reason))
}
