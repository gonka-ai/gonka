package app

import (
	"context"
	"fmt"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/nonces"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
)

// devshardWrites reports changed rows, so a replacement escrow routes without waiting for a poll.
type devshardWrites struct {
	store.Store
	changed func()
}

func (w devshardWrites) UpsertDevshard(ctx context.Context, record store.DevshardRecord) error {
	return w.report(w.Store.UpsertDevshard(ctx, record))
}

func (w devshardWrites) SetDevshardActive(ctx context.Context, escrowID string, active bool) error {
	return w.report(w.Store.SetDevshardActive(ctx, escrowID, active))
}

func (w devshardWrites) MarkDevshardGoneFromChain(ctx context.Context, escrowID string) error {
	return w.report(w.Store.MarkDevshardGoneFromChain(ctx, escrowID))
}

func (w devshardWrites) DeleteDevshard(ctx context.Context, escrowID string) error {
	return w.report(w.Store.DeleteDevshard(ctx, escrowID))
}

// ParkForSettlement reports the park, so a pass that listed the row active before it is followed by one that does not.
func (w devshardWrites) ParkForSettlement(ctx context.Context, escrowID string) error {
	return w.report(w.Store.ParkForSettlement(ctx, escrowID))
}

func (w devshardWrites) ParkForSettlementIfActive(ctx context.Context, escrowID string) (bool, error) {
	parked, err := w.Store.ParkForSettlementIfActive(ctx, escrowID)
	if parked {
		w.changed()
	}
	return parked, err
}

func (w devshardWrites) report(err error) error {
	if err == nil {
		w.changed()
	}
	return err
}

// depletionNotice breaks the registry/manager cycle for both rotation notices: the manager settles through the registry.
type depletionNotice struct{ manager *escrow.Manager }

func (d *depletionNotice) OnBalanceExhausted(escrowID string, reason scheduler.ExhaustionReason) {
	if d.manager != nil {
		d.manager.OnBalanceExhausted(escrowID, reason)
	}
}

func (d *depletionNotice) OnReserveTaken(escrowID string) {
	if d.manager != nil {
		d.manager.OnReserveTaken(escrowID)
	}
}

type escrowEpochLookup interface {
	GetEscrow(ctx context.Context, escrowID string) (chain.EscrowInfo, bool, error)
}

type devshardLookup interface {
	Devshard(ctx context.Context, escrowID string) (store.DevshardRecord, bool, error)
}

// creationEpochOf resolves the epoch the chain stamped on an escrow: the stored chain epoch, else the chain's answer, else the row's label. See ../nonces/README.md, "The judgements it does make".
func creationEpochOf(chainClient escrowEpochLookup, records devshardLookup) nonces.CreationEpochFunc {
	return func(ctx context.Context, escrowID string) (uint64, bool) {
		row, stored, err := records.Devshard(ctx, escrowID)
		stored = stored && err == nil
		if stored && row.ChainEpoch > 0 {
			return row.ChainEpoch, true
		}
		if info, found, err := chainClient.GetEscrow(ctx, escrowID); err == nil && found && info.EpochIndex > 0 {
			return info.EpochIndex, true
		}
		if stored && row.RotationEpoch > 0 {
			return uint64(row.RotationEpoch), true
		}
		return 0, false
	}
}

func findDevshard(ctx context.Context, records devshardLookup, escrowID string) (store.DevshardRecord, error) {
	record, found, err := records.Devshard(ctx, escrowID)
	if err != nil {
		return store.DevshardRecord{}, fmt.Errorf("reading devshard %s: %w", escrowID, err)
	}
	if !found {
		return store.DevshardRecord{}, fmt.Errorf("devshard %s: %w", escrowID, escrow.ErrUnknownEscrow)
	}
	return record, nil
}

const (
	storageDirectoryMode = 0o755
	storageFileMode      = 0o600
)
