package escrow

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"devshard/cmd/gateway/store"
)

// OnEscrowMissing marks an escrow a host reported as absent; the tick confirms it, so this hook does no I/O.
func (m *Manager) OnEscrowMissing(escrowID string) {
	m.missing.mark(escrowID)
}

func (m *Manager) checkMissing(ctx context.Context) error {
	var errs []error
	for _, escrowID := range slices.Sorted(maps.Keys(m.missing.drain())) {
		if err := m.TriggerEscrowCheck(ctx, escrowID); err != nil {
			m.OnEscrowMissing(escrowID) // a failed check must not un-schedule itself
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Only a confirmed not-found deactivates the escrow, and a confirmed settlement parks it. See README.md, "Escrow checks".
func (m *Manager) TriggerEscrowCheck(ctx context.Context, escrowID string) error {
	leave, busy := m.checks.enter(escrowID)
	if busy {
		return nil
	}
	defer leave()

	onChain, found, err := m.tx.GetEscrow(ctx, escrowID)
	if err != nil {
		return fmt.Errorf("checking escrow %s: %w", escrowID, err)
	}
	if found && onChain.Settled {
		if err := m.park(ctx, escrowID); !errors.Is(err, store.ErrDevshardNotFound) {
			return err
		}
		return nil
	}
	if found {
		return nil
	}
	// Routing stops before the row is written, so a failed write still takes the escrow out of service.
	if err := m.settlementSource.Retire(escrowID); err != nil {
		return fmt.Errorf("retiring escrow %s from routing: %w", escrowID, err)
	}
	if err := m.store.WithRetry(ctx, func() error {
		return m.store.MarkDevshardGoneFromChain(ctx, escrowID)
	}); err != nil {
		return fmt.Errorf("marking escrow %s gone from chain: %w", escrowID, err)
	}
	if m.narrator != nil {
		m.narrator.EscrowGoneFromChain(escrowID)
	}
	return nil
}
