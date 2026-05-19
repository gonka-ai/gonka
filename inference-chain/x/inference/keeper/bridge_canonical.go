package keeper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// buildCanonicalBridgeTransaction normalizes MsgBridgeExchange fields before they
// are hashed, stored, or used for quorum aggregation.
func buildCanonicalBridgeTransaction(msg *types.MsgBridgeExchange) (*types.BridgeTransaction, error) {
	blockNumber, err := canonicalDecimalString(msg.BlockNumber)
	if err != nil {
		return nil, fmt.Errorf("invalid blockNumber: %w", err)
	}
	receiptIndex, err := canonicalDecimalString(msg.ReceiptIndex)
	if err != nil {
		return nil, fmt.Errorf("invalid receiptIndex: %w", err)
	}
	amount, err := canonicalDecimalString(msg.Amount)
	if err != nil {
		return nil, fmt.Errorf("invalid amount: %w", err)
	}
	if bi, ok := new(big.Int).SetString(amount, 10); !ok || bi.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be > 0")
	}

	ownerAddr, err := sdk.AccAddressFromBech32(msg.OwnerAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid ownerAddress: %w", err)
	}

	receiptsRoot := strings.ToLower(msg.ReceiptsRoot)

	return &types.BridgeTransaction{
		ChainId:         strings.ToLower(msg.OriginChain),
		ContractAddress: strings.ToLower(msg.ContractAddress),
		OwnerAddress:    ownerAddr.String(),
		Amount:          amount,
		BlockNumber:     blockNumber,
		ReceiptIndex:    receiptIndex,
		ReceiptsRoot:    receiptsRoot,
	}, nil
}

// canonicalDecimalString strips leading zeros while preserving a single "0" digit.
func canonicalDecimalString(s string) (string, error) {
	bi, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return "", fmt.Errorf("not a base-10 integer")
	}
	if bi.Sign() < 0 {
		return "", fmt.Errorf("must be non-negative")
	}
	return bi.String(), nil
}

// canonicalBridgeDepositEventID identifies one L1 deposit receipt independent of
// ReceiptsRoot string variants used in the vote-aggregation key.
func canonicalBridgeDepositEventID(tx *types.BridgeTransaction) string {
	canon := fmt.Sprintf(
		"%s|%s|%s|%s|%s|%s",
		strings.ToLower(tx.ChainId),
		tx.BlockNumber,
		tx.ReceiptIndex,
		strings.ToLower(tx.ContractAddress),
		tx.OwnerAddress,
		tx.Amount,
	)
	hash := sha256.Sum256([]byte(canon))
	return hex.EncodeToString(hash[:])
}

func (k Keeper) isBridgeDepositEventCompleted(ctx context.Context, tx *types.BridgeTransaction) (bool, error) {
	eventID := canonicalBridgeDepositEventID(tx)
	return k.BridgeCompletedDepositEvents.Has(ctx, eventID)
}

func (k Keeper) markBridgeDepositEventCompleted(ctx context.Context, tx *types.BridgeTransaction) error {
	eventID := canonicalBridgeDepositEventID(tx)
	return k.BridgeCompletedDepositEvents.Set(ctx, eventID)
}
