package types

// DONTCOVER

import (
	sdkerrors "cosmossdk.io/errors"
)

// x/inference module sentinel errors
var (
	ErrInvalidSigner                         = sdkerrors.Register(ModuleName, 1100, "expected gov account as only signer for proposal message")
	ErrInferenceIdExists                     = sdkerrors.Register(ModuleName, 1101, "inference with id already exists")
	ErrInferenceNotFound                     = sdkerrors.Register(ModuleName, 1102, "inference with id not started")
	ErrParticipantNotFound                   = sdkerrors.Register(ModuleName, 1103, "participant not found")
	ErrInferenceNotFinished                  = sdkerrors.Register(ModuleName, 1104, "inference not finished")
	ErrParticipantCannotValidateOwnInference = sdkerrors.Register(ModuleName, 1105, "participant cannot validate own inference")
)
