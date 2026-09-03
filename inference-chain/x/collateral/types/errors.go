package types

import (
	sdkerrors "cosmossdk.io/errors"
)

// x/collateral module sentinel errors
var (
	ErrNoCollateralFound      = sdkerrors.Register(ModuleName, 1100, "no collateral found")
	ErrInsufficientCollateral = sdkerrors.Register(ModuleName, 1101, "insufficient collateral")
	ErrInvalidDenom           = sdkerrors.Register(ModuleName, 1102, "invalid denomination")
	ErrLatestEpochNotFound    = sdkerrors.Register(ModuleName, 1103, "latest epoch not found")
	ErrInvalidSigner          = sdkerrors.Register(ModuleName, 1104, "expected gov account as only signer for proposal message")
	ErrActiveTrainReservation = sdkerrors.Register(ModuleName, 1105, "cannot withdraw collateral while a node is reserved by an active trainshard")
)
