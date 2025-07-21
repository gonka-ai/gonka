package types

// DONTCOVER

import (
	sdkerrors "cosmossdk.io/errors"
)

// x/inference module sentinel errors
var (
	ErrInvalidSigner                           = sdkerrors.Register(ModuleName, 1100, "expected gov account as only signer for proposal message")
	ErrInferenceIdExists                       = sdkerrors.Register(ModuleName, 1101, "inference with id already exists")
	ErrInferenceNotFound                       = sdkerrors.Register(ModuleName, 1102, "inference with id not found")
	ErrParticipantNotFound                     = sdkerrors.Register(ModuleName, 1103, "participant not found")
	ErrInferenceNotFinished                    = sdkerrors.Register(ModuleName, 1104, "inference not finished")
	ErrParticipantCannotValidateOwnInference   = sdkerrors.Register(ModuleName, 1105, "participant cannot validate own inference")
	ErrRequesterCannotPay                      = sdkerrors.Register(ModuleName, 1106, "requester cannot pay for inference")
	ErrPocWrongStartBlockHeight                = sdkerrors.Register(ModuleName, 1107, "start block height must be divisible by 240")
	ErrPocTooLate                              = sdkerrors.Register(ModuleName, 1108, "POC submission is too late")
	ErrPocNonceNotAccepted                     = sdkerrors.Register(ModuleName, 1109, "POC nonce not accepted")
	ErrAccountAlreadyExists                    = sdkerrors.Register(ModuleName, 1110, "account already exists")
	ErrInferenceAlreadyInvalidated             = sdkerrors.Register(ModuleName, 1111, "inference already invalidated")
	ErrCurrentEpochGroupNotFound               = sdkerrors.Register(ModuleName, 1112, "current epoch group not found")
	ErrPocAddressInvalid                       = sdkerrors.Register(ModuleName, 1113, "POC address marked invalid, no longer allowed")
	ErrNegativeCoinBalance                     = sdkerrors.Register(ModuleName, 1114, "negative coin balance")
	ErrNegativeRefundBalance                   = sdkerrors.Register(ModuleName, 1115, "negative refund balance")
	ErrClaimSignatureInvalid                   = sdkerrors.Register(ModuleName, 1116, "claim signature invalid")
	ErrValidationsMissed                       = sdkerrors.Register(ModuleName, 1117, "validations missed")
	ErrTokenomicsNotFound                      = sdkerrors.Register(ModuleName, 1118, "tokenomics not found")
	ErrCannotMintNegativeCoins                 = sdkerrors.Register(ModuleName, 1119, "cannot mint negative coins")
	ErrTrainingTaskNotFound                    = sdkerrors.Register(ModuleName, 1120, "training task not found")
	ErrTrainingTaskAlreadyClaimedForAssignment = sdkerrors.Register(ModuleName, 1121, "training task already claimed for assignment")
	ErrTrainingTaskAlreadyAssigned             = sdkerrors.Register(ModuleName, 1122, "training task already assigned")
	ErrCannotCreateSubGroupFromSubGroup        = sdkerrors.Register(ModuleName, 1123, "cannot create a sub-group from a sub-group")
	ErrCannotGetSubGroupFromSubGroup           = sdkerrors.Register(ModuleName, 1124, "cannot get a sub-group from a sub-group")
	ErrInferenceHasInvalidModel                = sdkerrors.Register(ModuleName, 1125, "inference has a model that has no sub-group")
	ErrEpochGroupDataNotFound                  = sdkerrors.Register(ModuleName, 1126, "epoch group data not found for the given poc start block height and model id")
	ErrEffectiveEpochNotFound                  = sdkerrors.Register(ModuleName, 1127, "current epoch not found")
	ErrUpcomingEpochNotFound                   = sdkerrors.Register(ModuleName, 1128, "upcoming epoch group not found")
	ErrPreviousEpochNotFound                   = sdkerrors.Register(ModuleName, 1129, "previous epoch group not found")
	ErrLatestEpochNotFound                     = sdkerrors.Register(ModuleName, 1130, "latest epoch group data not found")
	ErrEpochGroupDataAlreadyExists             = sdkerrors.Register(ModuleName, 1131, "epoch group data already exists for the given poc start block height and model id")
	ErrInvalidSignature                        = sdkerrors.Register(ModuleName, 1132, "invalid keys provided for StartInference")
	ErrInferenceExpired                        = sdkerrors.Register(ModuleName, 1133, "inference has expired")
)
