package keeper

import (
	"context"
	"errors"
	"strings"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/types"
)

// BridgeRecheckPerBlock bounds how many pending inbound records EndBlock re-evaluates.
const BridgeRecheckPerBlock = 50

// MarkBridgeEpochForRecheck queues the epoch's pending records for re-evaluation:
// a removed member can lower RequiredPower below what the live voters already hold.
// A pass already under way keeps its cursor and starts over once it reaches the end.
func (k Keeper) MarkBridgeEpochForRecheck(ctx context.Context, epochIndex uint64) {
	cursor, err := k.BridgeRecheckEpochs.Get(ctx, epochIndex)
	if err == nil && cursor != "" {
		if err := k.BridgeRecheckRestart.Set(ctx, epochIndex); err != nil {
			k.LogError("Bridge recheck: failed to queue restart", types.Messages, "epochIndex", epochIndex, "error", err)
		}
		return
	}
	if err := k.BridgeRecheckEpochs.Set(ctx, epochIndex, ""); err != nil {
		k.LogError("Bridge recheck: failed to mark epoch", types.Messages, "epochIndex", epochIndex, "error", err)
	}
}

// RecheckPendingBridgeTransactions completes pending records of marked epochs that
// now meet RequiredPower. At most BridgeRecheckPerBlock records per call.
func (k Keeper) RecheckPendingBridgeTransactions(ctx sdk.Context) {
	budget := BridgeRecheckPerBlock
	epochs, err := k.BridgeRecheckEpochs.Iterate(ctx, nil)
	if err != nil {
		k.LogError("Bridge recheck: failed to iterate marked epochs", types.Messages, "error", err)
		return
	}
	marked, err := epochs.KeyValues()
	epochs.Close()
	if err != nil {
		k.LogError("Bridge recheck: failed to read marked epochs", types.Messages, "error", err)
		return
	}
	for _, kv := range marked {
		if budget == 0 {
			return
		}
		epochIndex, cursor := kv.Key, kv.Value
		last, done := k.recheckEpoch(ctx, epochIndex, cursor, &budget)
		if done {
			k.finishBridgeRecheckPass(ctx, epochIndex)
		} else {
			_ = k.BridgeRecheckEpochs.Set(ctx, epochIndex, last)
		}
	}
}

// finishBridgeRecheckPass drops the epoch mark, or rewinds the cursor when the
// epoch was marked again while the pass ran.
func (k Keeper) finishBridgeRecheckPass(ctx sdk.Context, epochIndex uint64) {
	again, err := k.BridgeRecheckRestart.Has(ctx, epochIndex)
	if err == nil && again {
		_ = k.BridgeRecheckRestart.Remove(ctx, epochIndex)
		_ = k.BridgeRecheckEpochs.Set(ctx, epochIndex, "")
		return
	}
	_ = k.BridgeRecheckEpochs.Remove(ctx, epochIndex)
}

// recheckEpoch walks the epoch's pending index after cursor; done is false when the budget ran out.
func (k Keeper) recheckEpoch(ctx sdk.Context, epochIndex uint64, cursor string, budget *int) (last string, done bool) {
	rng := collections.NewPrefixedPairRange[uint64, string](epochIndex)
	if cursor != "" {
		rng = rng.StartExclusive(cursor)
	}
	it, err := k.BridgePendingByEpoch.Iterate(ctx, rng)
	if err != nil {
		k.LogError("Bridge recheck: failed to iterate pending index", types.Messages, "epochIndex", epochIndex, "error", err)
		return cursor, true
	}
	// Read at most budget+1 keys: the extra one only tells whether the pass ends here.
	var keys []collections.Pair[uint64, string]
	for ; it.Valid() && len(keys) <= *budget; it.Next() {
		key, err := it.Key()
		if err != nil {
			it.Close()
			k.LogError("Bridge recheck: failed to read pending index", types.Messages, "epochIndex", epochIndex, "error", err)
			return cursor, true
		}
		keys = append(keys, key)
	}
	it.Close()
	if len(keys) == 0 {
		return cursor, true
	}

	epochGroup, err := k.GetEpochGroup(ctx, epochIndex, "")
	if err != nil {
		k.LogError("Bridge recheck: epoch group not found", types.Messages, "epochIndex", epochIndex, "error", err)
		return cursor, true
	}
	members, err := epochGroup.GetGroupMembers(ctx)
	if err != nil {
		k.LogError("Bridge recheck: failed to read group members", types.Messages, "epochIndex", epochIndex, "error", err)
		return cursor, false
	}
	required := bridgeRequiredPower(epochGroup.GroupData.TotalWeight, liveGroupWeight(members))

	last = cursor
	for _, key := range keys {
		if *budget == 0 {
			return last, false
		}
		*budget--
		last = key.K2()
		k.recheckBridgeTransaction(ctx, epochIndex, key.K2(), members, required)
	}
	return last, true
}

func (k Keeper) recheckBridgeTransaction(ctx sdk.Context, epochIndex uint64, id string, members []*group.GroupMember, required int64) {
	tx, found := k.getBridgeTransactionByID(ctx, id)
	if !found || tx.Status != types.BridgeTransactionStatus_BRIDGE_PENDING {
		k.removeBridgePending(ctx, epochIndex, id)
		return
	}
	k.hydrateBridgeTransactionValidators(ctx, tx)
	voted := liveVotedPower(tx.Validators, members)
	if voted < required {
		return
	}

	cacheCtx, write := ctx.CacheContext()
	tx.Status = types.BridgeTransactionStatus_BRIDGE_COMPLETED
	tx.Validators = nil
	k.SetBridgeTransaction(cacheCtx, tx)
	if err := k.handleCompletedBridgeTransaction(cacheCtx, tx); err != nil {
		k.LogError("Bridge recheck: release failed, record stays pending", types.Messages, "id", id, "error", err)
		return
	}
	write()
	k.removeBridgePending(ctx, epochIndex, id)
	k.LogInfo("Bridge recheck: transaction reached majority after group change", types.Messages,
		"id", id, "epochIndex", epochIndex, "powerRequired", required, "powerReceived", voted)
}

func (k Keeper) addBridgePending(ctx context.Context, epochIndex uint64, id string) {
	if err := k.BridgePendingByEpoch.Set(ctx, collections.Join(epochIndex, id)); err != nil {
		k.LogError("Bridge exchange: failed to index pending transaction", types.Messages, "id", id, "error", err)
	}
}

func (k Keeper) removeBridgePending(ctx context.Context, epochIndex uint64, id string) {
	if err := k.BridgePendingByEpoch.Remove(ctx, collections.Join(epochIndex, id)); err != nil && !errors.Is(err, collections.ErrNotFound) {
		k.LogError("Bridge exchange: failed to unindex transaction", types.Messages, "id", id, "error", err)
	}
}

func (k Keeper) getBridgeTransactionByID(ctx context.Context, id string) (*types.BridgeTransaction, bool) {
	parts := strings.SplitN(id, "_", 3)
	if len(parts) != 3 {
		return nil, false
	}
	tx, err := k.BridgeTransactionsMap.Get(ctx, collections.Join3(parts[0], parts[1], parts[2]))
	if err != nil {
		return nil, false
	}
	return &tx, true
}

// BackfillBridgePendingIndex indexes records that were pending before the index existed
// and marks their epochs, so the next EndBlock passes re-check them.
func (k Keeper) BackfillBridgePendingIndex(ctx context.Context) (int, error) {
	it, err := k.BridgeTransactionsMap.Iterate(ctx, nil)
	if err != nil {
		return 0, err
	}
	txs, err := it.Values()
	it.Close()
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range txs {
		tx := &txs[i]
		if tx.Status != types.BridgeTransactionStatus_BRIDGE_PENDING {
			continue
		}
		_, id, _, err := buildBridgeTransactionKey(tx)
		if err != nil {
			continue
		}
		k.addBridgePending(ctx, tx.EpochIndex, id)
		k.MarkBridgeEpochForRecheck(ctx, tx.EpochIndex)
		n++
	}
	return n, nil
}
