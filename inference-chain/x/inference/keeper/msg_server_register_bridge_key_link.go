package keeper

import (
	"bytes"
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RegisterBridgeKeyLink(goCtx context.Context, msg *types.MsgRegisterBridgeKeyLink) (*types.MsgRegisterBridgeKeyLinkResponse, error) {
	if err := k.CheckPermission(goCtx, msg, AccountPermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	chainID := ctx.ChainID()
	if chainID == "" {
		return nil, errorsmod.Wrap(types.ErrInvalidBridgeKeyLink, "chain id is empty")
	}

	ethPub, err := types.ParseCompressedSecp256k1PubKey(msg.EthPubKey)
	if err != nil {
		return nil, errorsmod.Wrap(types.ErrInvalidBridgeKeyLink, err.Error())
	}
	gonkaPub, err := types.ParseCompressedSecp256k1PubKey(msg.GonkaPubKey)
	if err != nil {
		return nil, errorsmod.Wrap(types.ErrInvalidBridgeKeyLink, err.Error())
	}
	creator, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, errorsmod.Wrapf(types.ErrInvalidBridgeKeyLink, "invalid creator: %v", err)
	}
	if msg.Creator != creator.String() {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidAddress, "creator must be canonical bech32")
	}
	derivedGonka, err := types.AddressFromCompressedPubKey(gonkaPub)
	if err != nil {
		return nil, errorsmod.Wrap(types.ErrInvalidBridgeKeyLink, err.Error())
	}
	if !derivedGonka.Equals(creator) {
		return nil, errorsmod.Wrap(types.ErrPubKeyDoesNotMatchAddress, "gonka public key does not match creator")
	}

	acc := k.AccountKeeper.GetAccount(ctx, creator)
	if acc != nil && acc.GetPubKey() != nil && !bytes.Equal(acc.GetPubKey().Bytes(), gonkaPub) {
		return nil, errorsmod.Wrap(types.ErrPubKeyDoesNotMatchAddress, "gonka public key does not match the account public key")
	}

	canonicalEth := types.CanonicalSecp256k1PubKeyHex(ethPub)
	payload := []byte(types.BridgeKeyLinkSignText(chainID, creator.String(), canonicalEth, msg.Nonce))
	recovered, err := recoverEthereumPersonalPubKey(payload, msg.EthSignature)
	if err != nil || !bytes.Equal(recovered, ethPub) {
		return nil, errorsmod.Wrap(types.ErrInvalidBridgeKeyLink, "ethereum signature does not recover to eth_pub_key")
	}

	ethDerived, err := types.AddressFromCompressedPubKey(ethPub)
	if err != nil {
		return nil, errorsmod.Wrap(types.ErrInvalidBridgeKeyLink, err.Error())
	}
	linkKey := ethDerived.String()
	gonkaAddress := creator.String()

	var currentNonce uint64
	previous, err := k.BridgeKeyLinks.Get(ctx, linkKey)
	if err != nil && !errorsIsNotFound(err) {
		return nil, err
	}
	if err == nil {
		currentNonce = previous.Nonce
	}
	if msg.Nonce <= currentNonce {
		return nil, errorsmod.Wrapf(types.ErrInvalidBridgeKeyLink, "nonce %d must be greater than current nonce %d", msg.Nonce, currentNonce)
	}

	if err := k.BridgeKeyLinks.Set(ctx, linkKey, types.BridgeKeyLink{
		OwnerAddress:     linkKey,
		RecipientAddress: gonkaAddress,
		Nonce:            msg.Nonce,
	}); err != nil {
		return nil, err
	}

	k.LogInfo("Bridge key link registered", types.Messages,
		"ownerAddress", linkKey,
		"recipientAddress", gonkaAddress,
		"nonce", msg.Nonce,
		"previous", previous.RecipientAddress,
		"chainId", chainID)

	return &types.MsgRegisterBridgeKeyLinkResponse{
		OwnerAddress:     linkKey,
		RecipientAddress: gonkaAddress,
		Nonce:            msg.Nonce,
	}, nil
}
