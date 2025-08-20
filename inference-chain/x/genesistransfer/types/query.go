package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/errors"
)

// ValidateBasic validates the QueryTransferStatusRequest
func (req *QueryTransferStatusRequest) ValidateBasic() error {
	if req.GenesisAddress == "" {
		return errors.ErrInvalidRequest.Wrap("genesis address cannot be empty")
	}

	if _, err := sdk.AccAddressFromBech32(req.GenesisAddress); err != nil {
		return errors.ErrInvalidAddress.Wrapf("invalid genesis address: %s", err.Error())
	}

	return nil
}

// ValidateBasic validates the QueryTransferEligibilityRequest
func (req *QueryTransferEligibilityRequest) ValidateBasic() error {
	if req.GenesisAddress == "" {
		return errors.ErrInvalidRequest.Wrap("genesis address cannot be empty")
	}

	if _, err := sdk.AccAddressFromBech32(req.GenesisAddress); err != nil {
		return errors.ErrInvalidAddress.Wrapf("invalid genesis address: %s", err.Error())
	}

	return nil
}

// ValidateBasic validates the QueryTransferHistoryRequest
func (req *QueryTransferHistoryRequest) ValidateBasic() error {
	// Pagination is optional, no specific validation needed
	return nil
}

// ValidateBasic validates the QueryAllowedAccountsRequest
func (req *QueryAllowedAccountsRequest) ValidateBasic() error {
	// No parameters to validate
	return nil
}
