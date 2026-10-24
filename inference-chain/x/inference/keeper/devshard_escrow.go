package keeper

import (
	"context"
	"encoding/hex"
	"fmt"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) StoreDevshardEscrow(ctx context.Context, escrow *types.DevshardEscrow, nextID uint64) (uint64, error) {
	return k.storeDevshardEscrow(ctx, escrow, nextID, k.GetDevshardEscrowEpochCount(ctx, escrow.EpochIndex))
}

// storeDevshardEscrow takes the epoch escrow count the caller has already read.
func (k Keeper) storeDevshardEscrow(ctx context.Context, escrow *types.DevshardEscrow, nextID uint64, epochCount uint64) (uint64, error) {
	escrow.Id = nextID

	stored, err := storedDevshardEscrow(*escrow)
	if err != nil {
		return 0, err
	}
	if err := k.DevshardEscrowCounter.Set(ctx, nextID); err != nil {
		return 0, err
	}
	if err := k.DevshardEscrows.Set(ctx, escrow.Id, stored); err != nil {
		return 0, err
	}
	if err := k.DevshardEscrowsByEpoch.Set(ctx, collections.Join(escrow.EpochIndex, escrow.Id), collections.NoValue{}); err != nil {
		return 0, err
	}
	if err := k.DevshardEscrowEpochCount.Set(ctx, escrow.EpochIndex, epochCount+1); err != nil {
		return 0, err
	}
	return escrow.Id, nil
}

func (k Keeper) GetDevshardEscrow(ctx context.Context, id uint64) (types.DevshardEscrow, bool) {
	v, err := k.DevshardEscrows.Get(ctx, id)
	if err != nil {
		return types.DevshardEscrow{}, false
	}
	if v, err = restoredDevshardEscrow(v); err != nil {
		return types.DevshardEscrow{}, false
	}
	if !v.Settled {
		if v.Settled, err = k.DevshardSettledEscrows.Has(ctx, id); err != nil {
			return types.DevshardEscrow{}, false
		}
	}
	return v, true
}

// MarkDevshardEscrowSettled records settlement in a key-only set instead of
// rewriting the whole escrow (~1 KB on mainnet) for one bool.
func (k Keeper) MarkDevshardEscrowSettled(ctx context.Context, id uint64) error {
	return k.DevshardSettledEscrows.Set(ctx, id)
}

func (k Keeper) SetDevshardEscrow(ctx context.Context, escrow types.DevshardEscrow) error {
	stored, err := storedDevshardEscrow(escrow)
	if err != nil {
		return err
	}
	return k.DevshardEscrows.Set(ctx, escrow.Id, stored)
}

// storedDevshardEscrow keeps each slot host once as address bytes plus a byte
// index per slot (mainnet: 16 slots, 4-12 hosts), creator and app hash as raw
// bytes; restoredDevshardEscrow undoes it. A string that does not round-trip
// stays as it is.
func storedDevshardEscrow(e types.DevshardEscrow) (types.DevshardEscrow, error) {
	if len(e.SlotHosts) > 0 || len(e.SlotIndex) > 0 || len(e.CreatorAddr) > 0 || len(e.AppHashRaw) > 0 {
		return e, fmt.Errorf("devshard escrow %d: slot_hosts, slot_index, creator_addr and app_hash_raw are storage-only", e.Id)
	}
	if b, ok := rawAddress(e.Creator); ok {
		e.CreatorAddr, e.Creator = b, ""
	}
	if b, err := hex.DecodeString(e.AppHash); err == nil && len(b) > 0 && hex.EncodeToString(b) == e.AppHash {
		e.AppHashRaw, e.AppHash = b, ""
	}
	if len(e.Slots) == 0 {
		return e, nil
	}
	hosts := make([][]byte, 0, len(e.Slots))
	pos := make(map[string]int, len(e.Slots))
	index := make([]byte, len(e.Slots))
	for i, addr := range e.Slots {
		j, ok := pos[addr]
		if !ok {
			b, valid := rawAddress(addr)
			j = len(hosts)
			if !valid || j > 0xff {
				return e, nil
			}
			pos[addr] = j
			hosts = append(hosts, b)
		}
		index[i] = byte(j)
	}
	e.SlotHosts, e.SlotIndex, e.Slots = hosts, index, nil
	return e, nil
}

// rawAddress returns the bytes of a bech32 address that encodes back to the same string.
func rawAddress(addr string) ([]byte, bool) {
	b, err := sdk.AccAddressFromBech32(addr)
	if err != nil || b.String() != addr {
		return nil, false
	}
	return b, true
}

func restoredDevshardEscrow(e types.DevshardEscrow) (types.DevshardEscrow, error) {
	if len(e.CreatorAddr) > 0 {
		e.Creator, e.CreatorAddr = sdk.AccAddress(e.CreatorAddr).String(), nil
	}
	if len(e.AppHashRaw) > 0 {
		e.AppHash, e.AppHashRaw = hex.EncodeToString(e.AppHashRaw), nil
	}
	if len(e.SlotIndex) == 0 {
		return e, nil
	}
	hosts := make([]string, len(e.SlotHosts))
	for i, b := range e.SlotHosts {
		hosts[i] = sdk.AccAddress(b).String()
	}
	slots := make([]string, len(e.SlotIndex))
	for i, j := range e.SlotIndex {
		if int(j) >= len(hosts) {
			return e, fmt.Errorf("devshard escrow %d: slot index %d out of %d hosts", e.Id, j, len(hosts))
		}
		slots[i] = hosts[j]
	}
	e.Slots, e.SlotHosts, e.SlotIndex = slots, nil, nil
	return e, nil
}

func (k Keeper) GetDevshardEscrowEpochCount(ctx context.Context, epochIndex uint64) uint64 {
	v, err := k.DevshardEscrowEpochCount.Get(ctx, epochIndex)
	if err != nil {
		return 0
	}
	return v
}

func (k Keeper) IncrementDevshardEscrowEpochCount(ctx context.Context, epochIndex uint64) error {
	count := k.GetDevshardEscrowEpochCount(ctx, epochIndex)
	return k.DevshardEscrowEpochCount.Set(ctx, epochIndex, count+1)
}
