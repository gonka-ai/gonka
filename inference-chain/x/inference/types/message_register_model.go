package types

import (
	"regexp"
	"strings"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgRegisterModel{}

// ModelIDRegex matches valid model IDs: alphanumeric, hyphens, underscores
var ModelIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// HfCommitRegex matches valid hex SHA commit hashes (40 hex chars)
var HfCommitRegex = regexp.MustCompile(`^[a-f0-9]{40}$`)

func NewMsgRegisterModel(authority string, proposedBy string, id string, unitsOfComputePerToken uint64) *MsgRegisterModel {
	return &MsgRegisterModel{
		Authority:              authority,
		ProposedBy:             proposedBy,
		Id:                     id,
		UnitsOfComputePerToken: unitsOfComputePerToken,
	}
}

func (msg *MsgRegisterModel) ValidateBasic() error {
	// authority bech32 signer
	if _, err := sdk.AccAddressFromBech32(msg.Authority); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid authority address (%s)", err)
	}

	// proposed_by bech32
	if _, err := sdk.AccAddressFromBech32(msg.ProposedBy); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid proposed_by address (%s)", err)
	}

	// id: non-empty, trimmed, max length, charset validation
	idTrimmed := strings.TrimSpace(msg.Id)
	if idTrimmed == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "id is required")
	}
	if len(idTrimmed) > MaxModelLen {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "id exceeds maximum length of %d", MaxModelLen)
	}
	if !ModelIDRegex.MatchString(idTrimmed) {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "id must contain only alphanumeric characters, hyphens, and underscores")
	}

	// units_of_compute_per_token > 0
	if msg.UnitsOfComputePerToken == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "units_of_compute_per_token must be > 0")
	}

	// hf_repo: valid URI or repo name, max length
	if msg.HfRepo != "" {
		hfRepoTrimmed := strings.TrimSpace(msg.HfRepo)
		if len(hfRepoTrimmed) > MaxUrlLen {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "hf_repo exceeds maximum length of %d", MaxUrlLen)
		}
		// Allow both URLs and simple repo names (e.g., "username/model-name")
		// Basic validation: no whitespace, reasonable format
		if strings.ContainsAny(hfRepoTrimmed, " \t\n\r") {
			return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "hf_repo cannot contain whitespace")
		}
	}

	// hf_commit: exact length/charset (SHA-like, 40 hex chars)
	if msg.HfCommit != "" {
		hfCommitTrimmed := strings.TrimSpace(msg.HfCommit)
		if len(hfCommitTrimmed) != HfCommitLength {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "hf_commit must be exactly %d hex characters", HfCommitLength)
		}
		if !HfCommitRegex.MatchString(strings.ToLower(hfCommitTrimmed)) {
			return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "hf_commit must be a valid hex SHA hash")
		}
	}

	// model_args: count bound, each non-empty and length bound
	if len(msg.ModelArgs) > MaxModelArgsCount {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "model_args exceeds maximum count of %d", MaxModelArgsCount)
	}
	for i, arg := range msg.ModelArgs {
		argTrimmed := strings.TrimSpace(arg)
		if argTrimmed == "" {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "model_args[%d] cannot be empty or whitespace", i)
		}
		if len(argTrimmed) > MaxModelArgLen {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "model_args[%d] exceeds maximum length of %d", i, MaxModelArgLen)
		}
	}

	// v_ram > 0
	if msg.VRam == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "v_ram must be > 0")
	}

	// throughput_per_nonce > 0
	if msg.ThroughputPerNonce == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "throughput_per_nonce must be > 0")
	}

	// validation_threshold within [0,1]
	if msg.ValidationThreshold != nil {
		thresholdValue := msg.ValidationThreshold.ToFloat()
		if thresholdValue < 0.0 || thresholdValue > 1.0 {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "validation_threshold must be in [0,1], got %f", thresholdValue)
		}
	}

	return nil
}
