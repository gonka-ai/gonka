package types

import (
	"math/big"
	"strings"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgRequestBridgeMint{}

func NewMsgRequestBridgeMint(creator, amount, destinationAddress, chainId string) *MsgRequestBridgeMint {
	return &MsgRequestBridgeMint{
		Creator:            creator,
		Amount:             amount,
		DestinationAddress: destinationAddress,
		ChainId:            chainId,
	}
}

func (msg *MsgRequestBridgeMint) ValidateBasic() error {
	// Validate creator address
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}

	// Validate amount is not empty and is a valid positive integer
	if len(msg.Amount) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "amount cannot be empty")
	}

	// Parse amount to ensure it's a valid positive integer
	amount := new(big.Int)
	_, ok := amount.SetString(msg.Amount, 10)
	if !ok {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "amount must be a valid integer")
	}
	if amount.Sign() <= 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "amount must be positive")
	}

	// Validate destination address is not empty
	if len(msg.DestinationAddress) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "destination address cannot be empty")
	}

	// Basic validation for Ethereum address format (0x + 40 hex characters)
	if !isValidEthereumAddress(msg.DestinationAddress) {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "destination address must be a valid Ethereum address")
	}

	// Validate chain ID is not empty and is supported
	if len(msg.ChainId) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "chain ID cannot be empty")
	}

	// Validate chain ID is in supported list
	if !isSupportedChainId(msg.ChainId) {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "unsupported chain ID: %s", msg.ChainId)
	}

	return nil
}

func (msg *MsgRequestBridgeMint) GetSigners() []sdk.AccAddress {
	creatorAddr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		panic(err)
	}
	return []sdk.AccAddress{creatorAddr}
}

// isValidEthereumAddress validates basic Ethereum address format
func isValidEthereumAddress(address string) bool {
	if len(address) != 42 {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(address), "0x") {
		return false
	}

	// Check if the rest are valid hex characters
	hexPart := address[2:]
	for _, r := range hexPart {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// isSupportedChainId checks if the chain ID is supported for bridging
func isSupportedChainId(chainId string) bool {
	supportedChains := map[string]bool{
		"ethereum": true,
		"sepolia":  true,
		"polygon":  true,
		"mumbai":   true,
		"arbitrum": true,
	}
	return supportedChains[chainId]
}
