package types

import (
	errorsmod "cosmossdk.io/errors"
)

var (
	ErrInvalidCapabilityName    = errorsmod.Register(ModuleName, 1002, "capability name not valid")
	ErrNilCapability            = errorsmod.Register(ModuleName, 1003, "provided capability is nil")
	ErrCapabilityTaken          = errorsmod.Register(ModuleName, 1004, "capability name already taken")
	ErrOwnerClaimed             = errorsmod.Register(ModuleName, 1005, "given owner already claimed capability")
	ErrCapabilityNotOwned       = errorsmod.Register(ModuleName, 1006, "capability not owned by module")
	ErrCapabilityNotFound       = errorsmod.Register(ModuleName, 1007, "capability not found")
	ErrCapabilityOwnersNotFound = errorsmod.Register(ModuleName, 1008, "owners not found for capability")
)
