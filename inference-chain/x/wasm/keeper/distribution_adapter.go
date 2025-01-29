package keeper

import (
	"context"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/types"

	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
)

type DistributionKeeperAdapter struct {
	keeper     *distrkeeper.Keeper
	grpcServer distrtypes.QueryServer
}

func NewDistributionKeeperAdapter(k *distrkeeper.Keeper) *DistributionKeeperAdapter {
	return &DistributionKeeperAdapter{
		keeper:     k,
		grpcServer: distrkeeper.NewQuerier(*k),
	}
}

func (d *DistributionKeeperAdapter) DelegationRewards(ctx context.Context, req *distrtypes.QueryDelegationRewardsRequest) (*distrtypes.QueryDelegationRewardsResponse, error) {
	return d.grpcServer.DelegationRewards(ctx, req)
}

func (d *DistributionKeeperAdapter) DelegationTotalRewards(ctx context.Context, req *distrtypes.QueryDelegationTotalRewardsRequest) (*distrtypes.QueryDelegationTotalRewardsResponse, error) {
	return d.grpcServer.DelegationTotalRewards(ctx, req)
}

func (d *DistributionKeeperAdapter) DelegatorValidators(ctx context.Context, req *distrtypes.QueryDelegatorValidatorsRequest) (*distrtypes.QueryDelegatorValidatorsResponse, error) {
	return d.grpcServer.DelegatorValidators(ctx, req)
}

func (d *DistributionKeeperAdapter) DelegatorWithdrawAddress(ctx context.Context, req *distrtypes.QueryDelegatorWithdrawAddressRequest) (*distrtypes.QueryDelegatorWithdrawAddressResponse, error) {
	return d.grpcServer.DelegatorWithdrawAddress(ctx, req)
}

func (d *DistributionKeeperAdapter) GetFeePool(ctx context.Context) (types.DecCoins, error) {
	feePool, err := d.keeper.FeePool.Get(ctx)
	if err != nil {
		return nil, err
	}
	return feePool.CommunityPool, nil
}

func (d *DistributionKeeperAdapter) GetCommunityTax(ctx context.Context) (math.LegacyDec, error) {
	tax, err := d.keeper.GetCommunityTax(ctx)
	return tax, err
}

func (d *DistributionKeeperAdapter) FundCommunityPool(ctx context.Context, amount types.Coins, sender types.AccAddress) error {
	return d.keeper.FundCommunityPool(ctx, amount, sender)
}

func (d *DistributionKeeperAdapter) GetParams(ctx context.Context) (distrtypes.Params, error) {
	params, err := d.keeper.Params.Get(ctx)
	return params, err
}
