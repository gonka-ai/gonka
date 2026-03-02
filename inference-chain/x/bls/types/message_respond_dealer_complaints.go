package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgRespondDealerComplaints{}

const (
	dealerComplaintResponseShareBytesLen       = 32
	dealerComplaintResponseOpeningMaterialLen  = 32
	maxDealerComplaintBatchResponsesPerMessage = 65536
)

func (m *MsgRespondDealerComplaints) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(m.Creator); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidAddress, "invalid creator address")
	}
	if m.EpochId == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "epoch_id must be > 0")
	}
	if len(m.Responses) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "responses must be non-empty")
	}
	if len(m.Responses) > maxDealerComplaintBatchResponsesPerMessage {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "responses exceeds maximum allowed count")
	}

	seenComplainers := make(map[uint32]struct{}, len(m.Responses))
	for i, response := range m.Responses {
		if _, exists := seenComplainers[response.ComplainerIndex]; exists {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "responses[%d] has duplicate complainer_index %d", i, response.ComplainerIndex)
		}
		seenComplainers[response.ComplainerIndex] = struct{}{}

		if len(response.ResponseShareBytes) != dealerComplaintResponseShareBytesLen {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "responses[%d].response_share_bytes must be exactly %d bytes", i, dealerComplaintResponseShareBytesLen)
		}
		if len(response.ResponseOpeningMaterial) != dealerComplaintResponseOpeningMaterialLen {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "responses[%d].response_opening_material must be exactly %d bytes", i, dealerComplaintResponseOpeningMaterialLen)
		}
	}

	return nil
}
