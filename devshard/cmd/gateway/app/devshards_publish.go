package app

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sync/errgroup"

	"devshard/bridge"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/store"
)

// publishEscrows brings the registry to what the store calls active, builders at a time. See operations.md, "Boot".
func (g *gateway) publishEscrows(ctx context.Context) (int, error) {
	// Taken before the list, so an escrow parked after the list read its row active is not reopened from it.
	listing := g.escrows.Listing()
	defer listing.Close()
	records, err := g.store.ListDevshards(ctx)
	if err != nil {
		return 0, fmt.Errorf("listing devshards: %w", err)
	}
	add := func(ctx context.Context, record store.DevshardRecord) error { return addServing(ctx, listing, record) }
	return publishEscrows(ctx, records, g.builders, add, g.escrows.Retire, unservableDeactivation(ctx, g.store), g.events.EscrowUnservable, g.events.EscrowUnopened)
}

type devshardDeactivator interface {
	MarkDevshardGoneFromChain(ctx context.Context, escrowID string) error
	DeleteDevshard(ctx context.Context, escrowID string) error
}

func unservableDeactivation(ctx context.Context, records devshardDeactivator) func(escrowID string, cause error) error {
	return func(escrowID string, cause error) error {
		if errors.Is(cause, bridge.ErrEscrowSettled) {
			return records.DeleteDevshard(ctx, escrowID)
		}
		return records.MarkDevshardGoneFromChain(ctx, escrowID)
	}
}

type escrowAdder interface {
	Add(ctx context.Context, escrowID, model string) error
	AddReserve(ctx context.Context, escrowID, model string) error
}

// addServing publishes an active row, a reserve as one, so every path that routes a row reads its role.
func addServing(ctx context.Context, escrows escrowAdder, record store.DevshardRecord) error {
	if record.RotationRole == escrow.RoleReserve {
		return escrows.AddReserve(ctx, record.EscrowID, record.Model)
	}
	return escrows.Add(ctx, record.EscrowID, record.Model)
}

func publishEscrows(
	ctx context.Context,
	records []store.DevshardRecord,
	builders int,
	add func(ctx context.Context, record store.DevshardRecord) error,
	retire func(escrowID string) error,
	deactivate func(escrowID string, cause error) error,
	unservable func(escrowID string, err error),
	unopened func(escrowID string, err error),
) (int, error) {
	var (
		active   []store.DevshardRecord
		problems []error
	)
	for _, record := range records {
		if record.Active {
			active = append(active, record)
			continue
		}
		if err := retire(record.EscrowID); err != nil {
			problems = append(problems, fmt.Errorf("retiring inactive escrow %s: %w", record.EscrowID, err))
		}
	}

	built := make([]error, len(active))
	var building errgroup.Group
	building.SetLimit(builders)
	for index, record := range active {
		building.Go(func() error {
			built[index] = add(ctx, record)
			return nil
		})
	}
	_ = building.Wait()

	unopenedCount := 0
	for index, err := range built {
		escrowID := active[index].EscrowID
		switch {
		case err == nil:
		case errors.Is(err, bridge.ErrEscrowNotFound), errors.Is(err, bridge.ErrEscrowSettled):
			unservable(escrowID, err)
			if deactivateErr := deactivate(escrowID, err); deactivateErr != nil {
				problems = append(problems, fmt.Errorf("deactivating escrow %s: %w", escrowID, deactivateErr))
			}
		default:
			unopened(escrowID, err)
			unopenedCount++
		}
	}
	return unopenedCount, errors.Join(problems...)
}
