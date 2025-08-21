package types

// DONTCOVER

import (
	sdkerrors "cosmossdk.io/errors"
)

// x/genesistransfer module sentinel errors
var (
	ErrInvalidSigner            = sdkerrors.Register(ModuleName, 1100, "expected gov account as only signer for proposal message")
	ErrSample                   = sdkerrors.Register(ModuleName, 1101, "sample error")
	ErrDuplicateTransferRecord  = sdkerrors.Register(ModuleName, 1102, "duplicate transfer record for genesis address")
	ErrTransferAlreadyCompleted = sdkerrors.Register(ModuleName, 1103, "transfer already completed for this genesis account")
	ErrAccountNotTransferable   = sdkerrors.Register(ModuleName, 1104, "account not in transferable whitelist")
	ErrInvalidTransferRequest   = sdkerrors.Register(ModuleName, 1105, "invalid transfer request")
	ErrAccountNotFound          = sdkerrors.Register(ModuleName, 1106, "account not found")
	ErrInsufficientBalance      = sdkerrors.Register(ModuleName, 1107, "insufficient balance for transfer")
	ErrVestingTransferFailed    = sdkerrors.Register(ModuleName, 1108, "vesting schedule transfer failed")
	ErrInvalidTransfer          = sdkerrors.Register(ModuleName, 1109, "invalid transfer")
	ErrAlreadyTransferred       = sdkerrors.Register(ModuleName, 1110, "account already transferred")
	ErrNotInAllowedList         = sdkerrors.Register(ModuleName, 1111, "account not in allowed transfer list")
)
