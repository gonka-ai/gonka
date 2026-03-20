package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgRequestThresholdSignature{}

const (
	expectedBytes32Len            = 32
	maxThresholdSigningDataChunks = 1024
)

func (m *MsgRequestThresholdSignature) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(m.Creator); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidAddress, "invalid creator address")
	}
	if m.CurrentEpochId == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "current_epoch_id must be > 0")
	}
	if len(m.ChainId) != expectedBytes32Len {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "chain_id must be exactly 32 bytes")
	}
	if len(m.RequestId) != expectedBytes32Len {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "request_id must be exactly 32 bytes")
	}
	if len(m.Data) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "data must be non-empty")
	}
	if len(m.Data) > maxThresholdSigningDataChunks {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "data has too many elements")
	}
	for i, chunk := range m.Data {
		if len(chunk) != expectedBytes32Len {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "data[%d] must be exactly 32 bytes", i)
		}
	}
	return nil
}
