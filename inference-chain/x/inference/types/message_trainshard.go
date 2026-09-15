package types

import (
	"strings"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var (
	_ sdk.Msg = &MsgAutokickTrainshardNode{}
	_ sdk.Msg = &MsgRefreshTrainingNodeOptIn{}
)

const (
	pinnedDigestSeparator   = "@sha256:"
	pinnedDigestLen         = 64
	maxPinnedImageLen       = 512
	MaxRefreshOptInNodes    = 256
	maxAutokickReasonLen    = 256
	maxAutokickRequestIdLen = 128
)

func ValidatePinnedImage(image string) error {
	repository, digest, found := strings.Cut(image, pinnedDigestSeparator)
	if !found || repository == "" || len(digest) != pinnedDigestLen || len(image) > maxPinnedImageLen {
		return ErrTrainshardBaseImageInvalid.Wrap(image)
	}
	for _, c := range digest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ErrTrainshardBaseImageInvalid.Wrap(image)
		}
	}
	return nil
}

func (msg *MsgAutokickTrainshardNode) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	if _, err := sdk.AccAddressFromBech32(msg.Participant); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid participant address (%s)", err)
	}
	if msg.NodeId == "" {
		return ErrPocNodeIdEmpty
	}
	if msg.RequestId == "" || len(msg.RequestId) > maxAutokickRequestIdLen {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "request_id length must be in (0, %d]", maxAutokickRequestIdLen)
	}
	if len(msg.Reason) > maxAutokickReasonLen {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "reason length must not exceed %d", maxAutokickReasonLen)
	}
	return nil
}

func (msg *MsgRefreshTrainingNodeOptIn) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	if len(msg.NodeIds) == 0 || len(msg.NodeIds) > MaxRefreshOptInNodes {
		return ErrTrainshardOptInRequest.Wrapf("node_ids count must be in (0, %d]", MaxRefreshOptInNodes)
	}
	seen := make(map[string]bool, len(msg.NodeIds))
	for _, nodeId := range msg.NodeIds {
		if nodeId == "" {
			return ErrPocNodeIdEmpty
		}
		if seen[nodeId] {
			return ErrTrainshardOptInRequest.Wrapf("duplicate node_id %s", nodeId)
		}
		seen[nodeId] = true
	}
	return nil
}
