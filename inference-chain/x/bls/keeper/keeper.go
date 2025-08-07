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
	ActiveEpochIDKey = "active_epoch_id"
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

// SetActiveEpochID sets the current active epoch undergoing DKG
func (k Keeper) SetActiveEpochID(ctx sdk.Context, epochID uint64) {
	store := k.storeService.OpenKVStore(ctx)
	key := []byte(ActiveEpochIDKey)
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, epochID)
	store.Set(key, value)
}

// GetActiveEpochID returns the current active epoch undergoing DKG
// Returns 0 if no epoch is currently active
func (k Keeper) GetActiveEpochID(ctx sdk.Context) (uint64, bool) {
	store := k.storeService.OpenKVStore(ctx)
	key := []byte(ActiveEpochIDKey)

	value, err := store.Get(key)
	if err != nil || value == nil {
		return 0, false // No active epoch
	}

	return binary.BigEndian.Uint64(value), true
}

// ClearActiveEpochID removes the active epoch ID (no epoch is active)
func (k Keeper) ClearActiveEpochID(ctx sdk.Context) {
	store := k.storeService.OpenKVStore(ctx)
	key := []byte(ActiveEpochIDKey)

	err := store.Delete(key)
	if err != nil {
		k.Logger().Error("Failed to clear active epoch ID", "error", err)
	}
}
