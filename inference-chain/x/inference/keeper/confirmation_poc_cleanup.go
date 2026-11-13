package keeper

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/collections/codec"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/types"
)

const (
	confirmationCleanupValidations = iota
	confirmationCleanupStoreCommits
	confirmationCleanupWeightDistributions
	confirmationCleanupCollectionCount
)

// confirmationCleanupCursor is the durable position of confirmation-prefix
// cleanup. A missing item is the zero cursor, which starts at the first event.
type confirmationCleanupCursor struct {
	EpochIndex    uint64 `json:"epoch_index"`
	EventSequence uint64 `json:"event_sequence"`
	Collection    uint8  `json:"collection"`
}

func (c confirmationCleanupCursor) at(epoch, sequence uint64) confirmationCleanupCursor {
	return confirmationCleanupCursor{EpochIndex: epoch, EventSequence: sequence, Collection: 0}
}

func (c confirmationCleanupCursor) past(epoch, sequence uint64) (confirmationCleanupCursor, error) {
	if sequence == ^uint64(0) {
		return confirmationCleanupCursor{}, sdkerrors.Wrap(types.ErrIllegalState, "confirmation cleanup event sequence overflow")
	}
	return confirmationCleanupCursor{EpochIndex: epoch, EventSequence: sequence + 1, Collection: 0}, nil
}

func confirmationEventBefore(epoch, sequence uint64, cursor confirmationCleanupCursor) bool {
	if epoch != cursor.EpochIndex {
		return epoch < cursor.EpochIndex
	}
	return sequence < cursor.EventSequence
}

func confirmationCleanupCollectionName(collection uint8) string {
	switch collection {
	case confirmationCleanupValidations:
		return "validations"
	case confirmationCleanupStoreCommits:
		return "store_commits"
	case confirmationCleanupWeightDistributions:
		return "weight_distributions"
	default:
		return "unknown"
	}
}

// confirmationCleanupFailAfter, when positive, fails the Nth removal and then
// disables itself. Tests use it to prove a removal error does not advance the
// cursor. Zero disables the hook.
var confirmationCleanupFailAfter int

func guardConfirmationCleanupRemoval() error {
	if confirmationCleanupFailAfter == 0 {
		return nil
	}
	confirmationCleanupFailAfter--
	if confirmationCleanupFailAfter == 0 {
		return sdkerrors.Wrap(types.ErrIllegalState, "confirmation cleanup removal failed")
	}
	return nil
}

// PruneConfirmationPoC deletes completed confirmation-trigger rows across
// later blocks. One PocPruningMax budget covers validations, store commits,
// weight distributions, and signer counters. The upgrade handler does not
// call this.
func (k Keeper) PruneConfirmationPoC(ctx context.Context, currentEpochIndex int64) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	return k.pruneConfirmationPoCWithParams(ctx, params, currentEpochIndex)
}

func (k Keeper) pruneConfirmationPoCWithParams(ctx context.Context, params types.Params, currentEpochIndex int64) error {
	if params.PocParams == nil || params.EpochParams == nil {
		return sdkerrors.Wrap(types.ErrIllegalState, "confirmation cleanup requires poc and epoch params")
	}
	budget := params.EpochParams.PocPruningMax
	if budget <= 0 {
		return nil
	}
	threshold := params.PocParams.PocDataPruningEpochThreshold
	if currentEpochIndex < int64(threshold) {
		return nil
	}
	oldestEligible := uint64(currentEpochIndex - int64(threshold))

	cursor, err := k.getConfirmationCleanupCursor(ctx)
	if err != nil {
		return err
	}
	stored := cursor
	active, isActive, err := k.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		return err
	}

	// Start at the stored event. Earlier records stay behind the cursor and are
	// not decoded again on later blocks.
	iter, err := k.ConfirmationPoCEvents.Iterate(ctx, new(collections.Range[collections.Pair[uint64, uint64]]).
		StartInclusive(collections.Join(cursor.EpochIndex, cursor.EventSequence)))
	if err != nil {
		return err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return err
		}
		event, err := iter.Value()
		if err != nil {
			return err
		}
		if confirmationEventBefore(key.K1(), key.K2(), cursor) {
			continue
		}
		if cursor.EpochIndex != key.K1() || cursor.EventSequence != key.K2() {
			cursor = cursor.at(key.K1(), key.K2())
		}
		if event.EpochIndex > oldestEligible {
			return k.persistConfirmationCleanupCursor(ctx, stored, cursor)
		}
		if isActive && active != nil && active.TriggerHeight == event.TriggerHeight {
			k.LogInfo("confirmation cleanup waiting on active trigger", types.Pruning,
				"epoch", event.EpochIndex,
				"eventSequence", event.EventSequence,
				"collection", confirmationCleanupCollectionName(cursor.Collection),
			)
			return k.persistConfirmationCleanupCursor(ctx, stored, cursor)
		}
		if event.Phase != types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED {
			k.LogInfo("confirmation cleanup left non-completed event", types.Pruning,
				"epoch", event.EpochIndex,
				"eventSequence", event.EventSequence,
			)
			next, err := cursor.past(event.EpochIndex, event.EventSequence)
			if err != nil {
				return err
			}
			cursor = next
			continue
		}
		if budget == 0 {
			return k.persistConfirmationCleanupCursor(ctx, stored, cursor)
		}

		for cursor.Collection < confirmationCleanupCollectionCount {
			removed, empty, err := k.deleteConfirmationPrefix(ctx, event.TriggerHeight, cursor.Collection, budget)
			if err != nil {
				return err
			}
			budget -= removed
			k.LogInfo("confirmation cleanup", types.Pruning,
				"epoch", cursor.EpochIndex,
				"eventSequence", cursor.EventSequence,
				"collection", confirmationCleanupCollectionName(cursor.Collection),
				"removed", removed,
				"budgetLeft", budget,
			)
			if !empty {
				return k.persistConfirmationCleanupCursor(ctx, stored, cursor)
			}
			cursor.Collection++
			if cursor.Collection >= confirmationCleanupCollectionCount {
				break
			}
			if budget == 0 {
				return k.persistConfirmationCleanupCursor(ctx, stored, cursor)
			}
		}
		next, err := cursor.past(event.EpochIndex, event.EventSequence)
		if err != nil {
			return err
		}
		cursor = next
	}
	return k.persistConfirmationCleanupCursor(ctx, stored, cursor)
}

func (k Keeper) deleteConfirmationPrefix(ctx context.Context, trigger int64, collection uint8, budget int64) (int64, bool, error) {
	if budget <= 0 {
		return 0, false, nil
	}
	switch collection {
	case confirmationCleanupValidations:
		return deletePrefixUntil(ctx, budget, k.PoCValidationsV2, collections.NewPrefixedTripleRange[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]](trigger), func(key collections.Triple[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]]) error {
			if err := guardConfirmationCleanupRemoval(); err != nil {
				return err
			}
			return k.PoCValidationsV2.Remove(ctx, key)
		})
	case confirmationCleanupStoreCommits:
		return deletePrefixUntil(ctx, budget, k.PoCV2StoreCommits, collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](trigger), func(key collections.Triple[int64, sdk.AccAddress, string]) error {
			if err := guardConfirmationCleanupRemoval(); err != nil {
				return err
			}
			return k.PoCV2StoreCommits.Remove(ctx, key)
		})
	case confirmationCleanupWeightDistributions:
		return deletePrefixUntil(ctx, budget, k.MLNodeWeightDistributions, collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](trigger), func(key collections.Triple[int64, sdk.AccAddress, string]) error {
			if err := guardConfirmationCleanupRemoval(); err != nil {
				return err
			}
			return k.MLNodeWeightDistributions.Remove(ctx, key)
		})
	default:
		return 0, false, sdkerrors.Wrapf(types.ErrIllegalState, "unknown confirmation cleanup collection %d", collection)
	}
}

func deletePrefixUntil[K any, V any](
	ctx context.Context,
	budget int64,
	list collections.Map[K, V],
	ranger collections.Ranger[K],
	remove func(K) error,
) (int64, bool, error) {
	iter, err := list.Iterate(ctx, ranger)
	if err != nil {
		return 0, false, err
	}
	defer iter.Close()
	var removed int64
	for ; iter.Valid() && removed < budget; iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return removed, false, err
		}
		if err := remove(key); err != nil {
			return removed, false, err
		}
		removed++
	}
	return removed, !iter.Valid(), nil
}

func (k Keeper) getConfirmationCleanupCursor(ctx context.Context) (confirmationCleanupCursor, error) {
	cursor, err := k.ConfirmationPoCCleanupCursor.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return confirmationCleanupCursor{}, nil
		}
		return confirmationCleanupCursor{}, err
	}
	return cursor, nil
}

func (k Keeper) persistConfirmationCleanupCursor(ctx context.Context, stored, cursor confirmationCleanupCursor) error {
	if stored == cursor {
		return nil
	}
	return k.ConfirmationPoCCleanupCursor.Set(ctx, cursor)
}

type confirmationCleanupCursorCodec struct{}

func (confirmationCleanupCursorCodec) Encode(value confirmationCleanupCursor) ([]byte, error) {
	if value.Collection >= confirmationCleanupCollectionCount {
		return nil, fmt.Errorf("confirmation cleanup collection %d", value.Collection)
	}
	buf := make([]byte, 17)
	binary.BigEndian.PutUint64(buf[0:8], value.EpochIndex)
	binary.BigEndian.PutUint64(buf[8:16], value.EventSequence)
	buf[16] = value.Collection
	return buf, nil
}

func (confirmationCleanupCursorCodec) Decode(b []byte) (confirmationCleanupCursor, error) {
	if len(b) != 17 {
		return confirmationCleanupCursor{}, fmt.Errorf("confirmation cleanup cursor length %d", len(b))
	}
	cursor := confirmationCleanupCursor{
		EpochIndex:    binary.BigEndian.Uint64(b[0:8]),
		EventSequence: binary.BigEndian.Uint64(b[8:16]),
		Collection:    b[16],
	}
	if cursor.Collection >= confirmationCleanupCollectionCount {
		return confirmationCleanupCursor{}, fmt.Errorf("confirmation cleanup collection %d", cursor.Collection)
	}
	return cursor, nil
}

func (confirmationCleanupCursorCodec) EncodeJSON(value confirmationCleanupCursor) ([]byte, error) {
	return json.Marshal(value)
}

func (confirmationCleanupCursorCodec) DecodeJSON(b []byte) (confirmationCleanupCursor, error) {
	var cursor confirmationCleanupCursor
	if err := json.Unmarshal(b, &cursor); err != nil {
		return confirmationCleanupCursor{}, err
	}
	if cursor.Collection >= confirmationCleanupCollectionCount {
		return confirmationCleanupCursor{}, fmt.Errorf("confirmation cleanup collection %d", cursor.Collection)
	}
	return cursor, nil
}

func (confirmationCleanupCursorCodec) Stringify(value confirmationCleanupCursor) string {
	return fmt.Sprintf("%d/%d/%s", value.EpochIndex, value.EventSequence, confirmationCleanupCollectionName(value.Collection))
}

func (confirmationCleanupCursorCodec) ValueType() string {
	return "productscience/inference/confirmationCleanupCursor"
}

var _ codec.ValueCodec[confirmationCleanupCursor] = confirmationCleanupCursorCodec{}
