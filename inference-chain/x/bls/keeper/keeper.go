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
	blsHooksState struct {
		hooks types.BlsHooks
	}

	Keeper struct {
		cdc          codec.BinaryCodec
		storeService store.KVStoreService
		logger       log.Logger
		hooksState   *blsHooksState

		// the address capable of executing a MsgUpdateParams message. Typically, this
		// should be the x/gov module account.
		authority string
	}
)

const (
	ActiveEpochIDKey         = "active_epoch_id"
	CurrentSigningEpochIDKey = "current_signing_epoch_id"
)

func NewKeeper(
	cdc codec.BinaryCodec,
	storeService store.KVStoreService,
	logger log.Logger,
	authority string,

) Keeper {
	if _, err := sdk.AccAddressFromBech32(authority); err != nil {
		//nolint:forbidigo
		//init code:
		panic(fmt.Sprintf("invalid authority address: %s", authority))
	}

	return Keeper{
		cdc:          cdc,
		storeService: storeService,
		authority:    authority,
		logger:       logger,
		hooksState:   &blsHooksState{},
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

func (k Keeper) Hooks() types.BlsHooks {
	if k.hooksState == nil || k.hooksState.hooks == nil {
		return types.MultiBlsHooks{}
	}
	return k.hooksState.hooks
}

func (k *Keeper) SetHooks(hooks types.BlsHooks) error {
	if k.hooksState == nil {
		k.hooksState = &blsHooksState{}
	}
	if k.hooksState.hooks != nil {
		return fmt.Errorf("cannot set bls hooks twice")
	}
	k.hooksState.hooks = hooks
	return nil
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

// SetCurrentSigningEpochID sets the epoch ID that external threshold-signing requests must use.
// This is expected to track the inference module's effective epoch.
func (k Keeper) SetCurrentSigningEpochID(ctx sdk.Context, epochID uint64) {
	store := k.storeService.OpenKVStore(ctx)
	key := []byte(CurrentSigningEpochIDKey)
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, epochID)
	store.Set(key, value)
}

// GetCurrentSigningEpochID returns the epoch ID that external threshold-signing requests must use.
func (k Keeper) GetCurrentSigningEpochID(ctx sdk.Context) (uint64, bool) {
	store := k.storeService.OpenKVStore(ctx)
	key := []byte(CurrentSigningEpochIDKey)

	value, err := store.Get(key)
	if err != nil || value == nil {
		return 0, false
	}

	return binary.BigEndian.Uint64(value), true
}
