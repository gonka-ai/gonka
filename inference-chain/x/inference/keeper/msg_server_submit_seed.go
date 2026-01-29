package keeper

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitSeed(goCtx context.Context, msg *types.MsgSubmitSeed) (*types.MsgSubmitSeedResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	seed := types.RandomSeed{
		Participant: msg.Creator,
		EpochIndex:  msg.EpochIndex,
		Signature:   msg.Signature,
	}

	if err := k.SetRandomSeed(ctx, seed); err != nil {
		return nil, err
	}

	return &types.MsgSubmitSeedResponse{}, nil
}

// validateSeedSignatureForSubmitSeed validates that the signature in msg matches the seed bytes
func (k msgServer) validateSeedSignatureForSubmitSeed(ctx sdk.Context, msg *types.MsgSubmitSeed) error {
	// Convert seed to bytes for signature verification
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, uint64(msg.Seed))

	// Decode signature from hex string
	signature, err := hex.DecodeString(msg.Signature)
	if err != nil {
		k.LogError("Error decoding signature", types.Claims, "error", err)
		return types.ErrSeedSignatureInvalid
	}

	// Get participant's public keys (including grantees)
	accountPubkeys, err := k.GetAccountPubKeysWithGrantees(ctx, msg.Creator)
	if err != nil {
		k.LogError("Error getting account pubkeys", types.Claims, "error", err)
		return err
	}

	// Try to verify signature with any of the participant's public keys
	for _, granteePubKeyStr := range accountPubkeys {
		pubKeyBytes, err := base64.StdEncoding.DecodeString(granteePubKeyStr)
		if err != nil {
			k.LogError("Error decoding pubkey", types.Claims, "error", err)
			continue
		}
		pubKey := &secp256k1.PubKey{Key: pubKeyBytes}

		k.LogDebug("Verifying seed signature", types.Claims,
			"seedBytes", hex.EncodeToString(seedBytes),
			"signature", hex.EncodeToString(signature),
			"pubkey", pubKey.String())

		if pubKey.VerifySignature(seedBytes, signature) {
			return nil // Signature is valid
		}
	}

	k.LogError("Seed signature validation failed for all pubkeys", types.Claims, "participant", msg.Creator)
	return types.ErrSeedSignatureInvalid
}
