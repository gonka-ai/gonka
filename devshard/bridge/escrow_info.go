package bridge

import (
	"encoding/hex"
	"fmt"
	"strconv"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// EscrowInfoFromQuery maps an on-chain DevshardEscrow query row onto the
// bind-time EscrowInfo. Gateway and host GetEscrow must use this so session
// config fields (including timeouts) cannot diverge.
func EscrowInfoFromQuery(id uint64, e *inferencetypes.DevshardEscrow) (*EscrowInfo, error) {
	if e == nil {
		return nil, fmt.Errorf("nil escrow")
	}
	appHash, err := hex.DecodeString(e.AppHash)
	if err != nil {
		return nil, fmt.Errorf("decode app_hash: %w", err)
	}
	slots := make([]string, len(e.Slots))
	copy(slots, e.Slots)
	return &EscrowInfo{
		EscrowID:                  strconv.FormatUint(id, 10),
		Amount:                    e.Amount,
		CreatorAddress:            e.Creator,
		AppHash:                   appHash,
		Slots:                     slots,
		ModelID:                   e.ModelId,
		TokenPrice:                e.TokenPrice,
		CreateDevshardFee:         e.CreateDevshardFee,
		FeePerNonce:               e.FeePerNonce,
		InferenceSealGraceNonces:  e.InferenceSealGraceNonces,
		InferenceSealGraceSeconds: e.InferenceSealGraceSeconds,
		AutoSealEveryNNonces:      e.AutoSealEveryNNonces,
		ValidationRate:            e.ValidationRate,
		VoteThresholdFactor:       e.VoteThresholdFactor,
		RefusalTimeout:            e.RefusalTimeout,
		ExecutionTimeout:          e.ExecutionTimeout,
		EpochID:                   e.EpochIndex,
		Settled:                   e.Settled,
	}, nil
}
