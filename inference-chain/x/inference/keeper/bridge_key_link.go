package keeper

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/productscience/inference/x/inference/types"
)

// ResolveBridgeRecipient returns the Gonka account linked to ownerAddress.
// ownerAddress is the address MsgBridgeExchange stored, derived from the
// Ethereum public key. With no link, ownerAddress itself is the recipient.
func (k Keeper) ResolveBridgeRecipient(ctx context.Context, ownerAddress string) (string, error) {
	linked, err := k.BridgeKeyLinks.Get(ctx, ownerAddress)
	if err != nil {
		if errorsIsNotFound(err) {
			return ownerAddress, nil
		}
		return "", err
	}
	if _, err := sdk.AccAddressFromBech32(linked); err != nil {
		return "", fmt.Errorf("invalid linked bridge recipient %s: %w", linked, err)
	}
	return linked, nil
}

// GetAllBridgeKeyLinks returns every link in owner-address order.
func (k Keeper) GetAllBridgeKeyLinks(ctx context.Context) ([]types.BridgeKeyLink, error) {
	var links []types.BridgeKeyLink
	err := k.BridgeKeyLinks.Walk(ctx, nil, func(owner, recipient string) (bool, error) {
		links = append(links, types.BridgeKeyLink{
			OwnerAddress:     owner,
			RecipientAddress: recipient,
		})
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(links, func(i, j int) bool {
		return links[i].OwnerAddress < links[j].OwnerAddress
	})
	return links, nil
}

func errorsIsNotFound(err error) bool {
	return errors.Is(err, collections.ErrNotFound)
}

// recoverEthereumPersonalPubKey recovers the compressed secp256k1 public key
// from a 65-byte Ethereum personal_sign signature (r || s || v).
func recoverEthereumPersonalPubKey(message, sig []byte) ([]byte, error) {
	if len(sig) != 65 {
		return nil, fmt.Errorf("eth signature must be 65 bytes")
	}
	v := sig[64]
	switch v {
	case 0, 1:
		v += 27
	case 27, 28:
	default:
		return nil, fmt.Errorf("invalid ethereum recovery id %d", v)
	}
	compact := make([]byte, 65)
	compact[0] = v
	copy(compact[1:], sig[:64])

	prefix := fmt.Sprintf("\x19Ethereum Signed Message:\n%d", len(message))
	hash := keccak256Hash(append([]byte(prefix), message...))
	pub, _, err := ecdsa.RecoverCompact(compact, hash[:])
	if err != nil {
		return nil, err
	}
	return pub.SerializeCompressed(), nil
}
