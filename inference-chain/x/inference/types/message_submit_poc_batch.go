package types

import (
	"math"
	"strings"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitPocBatch{}

// Maximum limits for PoC batch to prevent state bloat and block-space DoS
const (
	// MaxPocBatchNonces is the maximum number of nonces allowed per batch
	MaxPocBatchNonces = 10000
	// MaxPocBatchIdLength is the maximum length of batch ID string
	MaxPocBatchIdLength = 128
	// MaxPocNodeIdLength is the maximum length of node ID string
	MaxPocNodeIdLength = 128
)

func NewMsgSubmitPocBatch(creator string, pocStageStartBlockHeight int64, batchID string, nonces []int64, dist []float64, nodeID string) *MsgSubmitPocBatch {
	return &MsgSubmitPocBatch{
		Creator:                  creator,
		PocStageStartBlockHeight: pocStageStartBlockHeight,
		BatchId:                  batchID,
		Nonces:                   nonces,
		Dist:                     dist,
		NodeId:                   nodeID,
	}
}

func (msg *MsgSubmitPocBatch) ValidateBasic() error {
	// signer
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// height > 0
	if msg.PocStageStartBlockHeight <= 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "poc_stage_start_block_height must be > 0")
	}
	// batch_id required and bounded
	if strings.TrimSpace(msg.BatchId) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "batch_id is required")
	}
	if len(msg.BatchId) > MaxPocBatchIdLength {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "batch_id exceeds maximum length of %d", MaxPocBatchIdLength)
	}
	// node_id bounded (if provided)
	if len(msg.NodeId) > MaxPocNodeIdLength {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "node_id exceeds maximum length of %d", MaxPocNodeIdLength)
	}
	// nonces required, bounded, and each >= 0
	if len(msg.Nonces) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "nonces must be non-empty")
	}
	if len(msg.Nonces) > MaxPocBatchNonces {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "nonces exceeds maximum count of %d", MaxPocBatchNonces)
	}
	for i, n := range msg.Nonces {
		if n < 0 {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "nonces[%d] must be >= 0", i)
		}
	}
	// dist required, bounded, and values finite and >= 0
	if len(msg.Dist) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "dist must be non-empty")
	}
	if len(msg.Dist) > MaxPocBatchNonces {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "dist exceeds maximum count of %d", MaxPocBatchNonces)
	}
	for i, d := range msg.Dist {
		if math.IsNaN(d) || math.IsInf(d, 0) {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "dist[%d] must be finite", i)
		}
		if d < 0 {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "dist[%d] must be >= 0", i)
		}
	}
	// shape relation
	if len(msg.Nonces) != len(msg.Dist) {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "nonces and dist must have the same length: %d != %d", len(msg.Nonces), len(msg.Dist))
	}
	return nil
}
