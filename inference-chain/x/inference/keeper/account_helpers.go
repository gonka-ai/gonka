package keeper

import (
	"context"
	"encoding/base64"
	"math"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/productscience/inference/x/inference/types"
)

func ensureParticipantEpochStats(participant *types.Participant) {
	if participant.CurrentEpochStats == nil {
		participant.CurrentEpochStats = types.NewCurrentEpochStats()
	}
}

func (k msgServer) AddToCoinBalance(ctx context.Context, participant *types.Participant, amount uint64, memo string) error {
	if amount > uint64(math.MaxInt64) {
		return sdkerrors.Wrapf(types.ErrArithmeticOverflow, "amount %d exceeds int64 max", amount)
	}
	ensureParticipantEpochStats(participant)
	participant.CoinBalance += int64(amount)
	participant.CurrentEpochStats.EarnedCoins += amount
	k.SafeLogSubAccountTransactionUint(ctx, types.ModuleName, participant.Address, types.OwedSubAccount, amount, memo)
	return nil
}

func (k msgServer) GetAccountPubKey(ctx context.Context, address string) (string, error) {
	accAddr, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		return "", err
	}
	acc := k.AccountKeeper.GetAccount(ctx, accAddr)
	if acc == nil || acc.GetPubKey() == nil {
		return "", types.ErrPubKeyUnavailable
	}
	return base64.StdEncoding.EncodeToString(acc.GetPubKey().Bytes()), nil
}

func (k msgServer) GetAccountPubKeysWithGrantees(ctx context.Context, granterAddress string) ([]string, error) {
	pubkeys := make([]string, 0, 1)
	granterPubKey, err := k.GetAccountPubKey(ctx, granterAddress)
	if err != nil {
		return nil, err
	}
	pubkeys = append(pubkeys, granterPubKey)

	nextKey := []byte(nil)
	for {
		resp, err := k.AuthzKeeper.GranterGrants(ctx, &authztypes.QueryGranterGrantsRequest{
			Granter: granterAddress,
			Pagination: &query.PageRequest{
				Key: nextKey,
			},
		})
		if err != nil {
			return nil, err
		}
		for _, grant := range resp.Grants {
			if grant.Authorization == nil {
				continue
			}
			if _, ok := grant.Authorization.GetCachedValue().(*authztypes.GenericAuthorization); !ok {
				continue
			}
			granteePubKey, err := k.GetAccountPubKey(ctx, grant.Grantee)
			if err == nil {
				pubkeys = append(pubkeys, granteePubKey)
			}
		}
		if resp.Pagination == nil || len(resp.Pagination.NextKey) == 0 {
			break
		}
		nextKey = resp.Pagination.NextKey
	}
	return pubkeys, nil
}
