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
	*store.Store
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

type devshardLister interface {
	ListDevshards(ctx context.Context) ([]store.DevshardRecord, error)
}

// creationEpochOf resolves the epoch the chain stamped on an escrow: the stored chain epoch, else the chain's answer, else the row's label. See ../nonces/README.md, "The judgements it does make".
func creationEpochOf(chainClient escrowEpochLookup, records devshardLister) nonces.CreationEpochFunc {
	return func(ctx context.Context, escrowID string) (uint64, bool) {
		var row store.DevshardRecord
		stored := false
		if devshards, err := records.ListDevshards(ctx); err == nil {
			for _, record := range devshards {
				if record.EscrowID == escrowID {
					row, stored = record, true
					break
				}
			}
		}
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

type devshardLookup interface {
	ListDevshards(ctx context.Context) ([]store.DevshardRecord, error)
}

func findDevshard(ctx context.Context, records devshardLookup, escrowID string) (store.DevshardRecord, error) {
	known, err := records.ListDevshards(ctx)
	if err != nil {
		return store.DevshardRecord{}, fmt.Errorf("listing devshards: %w", err)
	}
	for _, record := range known {
		if record.EscrowID == escrowID {
			return record, nil
		}
	}
	return store.DevshardRecord{}, fmt.Errorf("devshard %s: %w", escrowID, escrow.ErrUnknownEscrow)
}

const (
	storageDirectoryMode = 0o755
	storageFileMode      = 0o600
)
