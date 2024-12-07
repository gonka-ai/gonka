package keeper

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) ClaimRewards(goCtx context.Context, msg *types.MsgClaimRewards) (*types.MsgClaimRewardsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	settleAmount, found := k.GetSettleAmount(ctx, msg.Creator)
	if !found {
		return &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "No rewards for this address",
		}, nil
	}
	if settleAmount.PocStartHeight != msg.PocStartHeight {
		return &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "No rewards for this block height",
		}, nil
	}

	err := k.validateClaim(ctx, msg, settleAmount)
	if err != nil {
		return nil, err
	}
	k.LogDebug("Claim verified", "account", msg.Creator, "seed", msg.Seed)

	totalCoins := settleAmount.GetTotalCoins()
	k.LogInfo("Issuing rewards", "address", msg.Creator, "amount", totalCoins)
	err = k.PayParticipantFromEscrow(ctx, msg.Creator, totalCoins)
	if err != nil {
		k.LogError("Error paying participant", "error", err)
		// Big question: do we remove the settle amount? Probably not
		return nil, err
	}
	k.RemoveSettleAmount(ctx, msg.Creator)

	return &types.MsgClaimRewardsResponse{
		Amount: totalCoins,
		Result: "Rewards claimed",
	}, nil
}

func (k msgServer) validateClaim(ctx sdk.Context, msg *types.MsgClaimRewards, settleAmount types.SettleAmount) error {
	k.LogInfo("Validating claim", "account", msg.Creator, "seed", msg.Seed, "height", msg.PocStartHeight)
	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return types.ErrPocAddressInvalid
	}
	acc := k.AccountKeeper.GetAccount(ctx, addr)

	if settleAmount.PocStartHeight != msg.PocStartHeight {
		k.LogError("Claim rewards height mismatch", "expected", settleAmount.PocStartHeight, "actual", msg.PocStartHeight)
		return types.ErrParticipantNotFound
	}
	pubKey := acc.GetPubKey()
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, uint64(msg.Seed))
	signature, err := hex.DecodeString(settleAmount.SeedSignature)
	if err != nil {
		k.LogError("Error decoding signature", "error", err)
		return err
	}
	k.LogDebug("Verifying signature", "seedBytes", hex.EncodeToString(seedBytes), "signature", hex.EncodeToString(signature), "pubkey", pubKey.String())
	if !pubKey.VerifySignature(seedBytes, signature) {
		k.LogError("Signature verification failed", "seed", msg.Seed, "signature", settleAmount.SeedSignature, "seedBytes", hex.EncodeToString(seedBytes))
		return types.ErrClaimSignatureInvalid
	}
	return nil
}
