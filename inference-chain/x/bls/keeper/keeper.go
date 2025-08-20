package keeper

import (
	"encoding/binary"
	"fmt"

	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/bls/types"
)

type (
	Keeper struct {
		cdc          codec.BinaryCodec
		storeService store.KVStoreService
		logger       log.Logger

		// the address capable of executing a MsgUpdateParams message. Typically, this
		// should be the x/gov module account.
		authority string
	}
)

const (
	ActiveEpochIndexKey = "active_epoch_index"
)

func NewKeeper(
	cdc codec.BinaryCodec,
	storeService store.KVStoreService,
	logger log.Logger,
	authority string,

) Keeper {
	if _, err := sdk.AccAddressFromBech32(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address: %s", authority))
	}

	return Keeper{
		cdc:          cdc,
		storeService: storeService,
		authority:    authority,
		logger:       logger,
	}
}

// GetAuthority returns the module's authority.
func (k Keeper) GetAuthority() string {
	return k.authority
}

// Logger returns a module-specific logger.
func (k Keeper) Logger() log.Logger {
	return k.logger.With("module", fmt.Sprintf("x/%s", types.ModuleName))
}

// SetActiveEpochIndex sets the current active epoch undergoing DKG
func (k Keeper) SetActiveEpochIndex(ctx sdk.Context, epochIndex uint64) {
	store := k.storeService.OpenKVStore(ctx)
	key := []byte(ActiveEpochIndexKey)
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, epochIndex)
	store.Set(key, value)
}

// GetActiveEpochIndex returns the current active epoch undergoing DKG
// Returns 0 if no epoch is currently active
func (k Keeper) GetActiveEpochIndex(ctx sdk.Context) (uint64, bool) {
	store := k.storeService.OpenKVStore(ctx)
	key := []byte(ActiveEpochIndexKey)

	value, err := store.Get(key)
	if err != nil || value == nil {
		return 0, false // No active epoch
	}

	return binary.BigEndian.Uint64(value), true
}

// ClearActiveEpochIndex removes the active epoch index (no epoch is active)
func (k Keeper) ClearActiveEpochIndex(ctx sdk.Context) {
	store := k.storeService.OpenKVStore(ctx)
	key := []byte(ActiveEpochIndexKey)

	err := store.Delete(key)
	if err != nil {
		k.Logger().Error("Failed to clear active epoch ID", "error", err)
	}
}
