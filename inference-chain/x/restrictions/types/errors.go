package types

// DONTCOVER

import (
	sdkerrors "cosmossdk.io/errors"
)

// x/restrictions module sentinel errors
var (
	ErrInvalidSigner               = sdkerrors.Register(ModuleName, 1100, "expected gov account as only signer for proposal message")
	ErrSample                      = sdkerrors.Register(ModuleName, 1101, "sample error")
	ErrExemptionNotFound           = sdkerrors.Register(ModuleName, 1102, "emergency transfer exemption not found")
	ErrExemptionExpired            = sdkerrors.Register(ModuleName, 1103, "emergency transfer exemption expired")
	ErrInvalidExemptionMatch       = sdkerrors.Register(ModuleName, 1104, "transfer does not match exemption template")
	ErrInvalidExemption            = sdkerrors.Register(ModuleName, 1105, "invalid exemption configuration")
	ErrExemptionAmountExceeded     = sdkerrors.Register(ModuleName, 1106, "transfer amount exceeds exemption maximum")
	ErrExemptionUsageLimitExceeded = sdkerrors.Register(ModuleName, 1107, "exemption usage limit exceeded")
	ErrTransferRestricted          = sdkerrors.Register(ModuleName, 1108, "transfer restricted during bootstrap period")
)
