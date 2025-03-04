package types

// DONTCOVER

import (
	sdkerrors "cosmossdk.io/errors"
)

// x/inference module sentinel errors
var (
	ErrInvalidSigner                         = sdkerrors.Register(ModuleName, 1100, "expected gov account as only signer for proposal message")
	ErrInferenceIdExists                     = sdkerrors.Register(ModuleName, 1101, "inference with id already exists")
	ErrInferenceNotFound                     = sdkerrors.Register(ModuleName, 1102, "inference with id not found")
	ErrParticipantNotFound                   = sdkerrors.Register(ModuleName, 1103, "participant not found")
	ErrInferenceNotFinished                  = sdkerrors.Register(ModuleName, 1104, "inference not finished")
	ErrParticipantCannotValidateOwnInference = sdkerrors.Register(ModuleName, 1105, "participant cannot validate own inference")
	ErrRequesterCannotPay                    = sdkerrors.Register(ModuleName, 1106, "requester cannot pay for inference")
	ErrPocWrongStartBlockHeight              = sdkerrors.Register(ModuleName, 1107, "start block height must be divisible by 240")
	ErrPocTooLate                            = sdkerrors.Register(ModuleName, 1108, "POC submission is too late")
	ErrPocNonceNotAccepted                   = sdkerrors.Register(ModuleName, 1109, "POC nonce not accepted")
	ErrAccountAlreadyExists                  = sdkerrors.Register(ModuleName, 1110, "account already exists")
	ErrInferenceAlreadyInvalidated           = sdkerrors.Register(ModuleName, 1111, "inference already invalidated")
	ErrCurrentEpochGroupNotFound             = sdkerrors.Register(ModuleName, 1112, "current epoch group not found")
	ErrPocAddressInvalid                     = sdkerrors.Register(ModuleName, 1113, "POC address marked invalid, no longer allowed")
	ErrNegativeCoinBalance                   = sdkerrors.Register(ModuleName, 1114, "negative coin balance")
	ErrNegativeRefundBalance                 = sdkerrors.Register(ModuleName, 1115, "negative refund balance")
	ErrClaimSignatureInvalid                 = sdkerrors.Register(ModuleName, 1116, "claim signature invalid")
	ErrValidationsMissed                     = sdkerrors.Register(ModuleName, 1117, "validations missed")
	ErrTokenomicsNotFound                    = sdkerrors.Register(ModuleName, 1118, "tokenomics not found")
	ErrCannotMintNegativeCoins               = sdkerrors.Register(ModuleName, 1119, "cannot mint negative coins")
)
