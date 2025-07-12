package keeper

import (
	"fmt"

	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/collateral/types"
)

type (
	Keeper struct {
		cdc          codec.BinaryCodec
		storeService store.KVStoreService
		logger       log.Logger

		// the address capable of executing a MsgUpdateParams message. Typically, this
		// should be the x/gov module account.
		authority string

		bankKeeper       types.BankKeeper
		bankEscrowKeeper types.BankEscrowKeeper
		stakingKeeper    types.StakingKeeper
		inferenceKeeper  types.InferenceKeeper
	}
)

func NewKeeper(
	cdc codec.BinaryCodec,
	storeService store.KVStoreService,
	logger log.Logger,
	authority string,

	bankKeeper types.BankKeeper,
	bankEscrowKeeper types.BankEscrowKeeper,
	stakingKeeper types.StakingKeeper,
	inferenceKeeper types.InferenceKeeper,
) Keeper {
	if _, err := sdk.AccAddressFromBech32(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address: %s", authority))
	}

	return Keeper{
		cdc:          cdc,
		storeService: storeService,
		authority:    authority,
		logger:       logger,

		bankKeeper:       bankKeeper,
		bankEscrowKeeper: bankEscrowKeeper,
		stakingKeeper:    stakingKeeper,
		inferenceKeeper:  inferenceKeeper,
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

// SetCollateral stores a participant's collateral amount
func (k Keeper) SetCollateral(ctx sdk.Context, participantAddress string, amount sdk.Coin) {
	store := k.storeService.OpenKVStore(ctx)
	bz := k.cdc.MustMarshal(&amount)
	err := store.Set(types.GetCollateralKey(participantAddress), bz)
	if err != nil {
		panic(err)
	}
}

// GetCollateral retrieves a participant's collateral amount
func (k Keeper) GetCollateral(ctx sdk.Context, participantAddress string) (sdk.Coin, bool) {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := store.Get(types.GetCollateralKey(participantAddress))
	if err != nil {
		panic(err)
	}
	if bz == nil {
		return sdk.Coin{}, false
	}

	var amount sdk.Coin
	k.cdc.MustUnmarshal(bz, &amount)
	return amount, true
}

// RemoveCollateral removes a participant's collateral from the store
func (k Keeper) RemoveCollateral(ctx sdk.Context, participantAddress string) {
	store := k.storeService.OpenKVStore(ctx)
	err := store.Delete(types.GetCollateralKey(participantAddress))
	if err != nil {
		panic(err)
	}
}

// GetAllCollateral returns all collateral entries
func (k Keeper) GetAllCollateral(ctx sdk.Context) map[string]sdk.Coin {
	store := k.storeService.OpenKVStore(ctx)
	collateralMap := make(map[string]sdk.Coin)

	iterator, err := store.Iterator(types.CollateralKey, nil)
	if err != nil {
		panic(err)
	}
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		// Extract participant address from the key
		key := iterator.Key()
		participantAddr := string(key[len(types.CollateralKey):])

		// Unmarshal the collateral amount
		var amount sdk.Coin
		k.cdc.MustUnmarshal(iterator.Value(), &amount)

		collateralMap[participantAddr] = amount
	}

	return collateralMap
}
