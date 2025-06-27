package types

import (
	"strings"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgBridgeExchange{}

func NewMsgBridgeExchange(
	validator string,
	originChain string,
	contractAddress string,
	ownerAddress string,
	amount string,
	blockNumber string,
	receiptIndex string,
	receiptsRoot string,
) *MsgBridgeExchange {
	return &MsgBridgeExchange{
		Validator:       validator,
		OriginChain:     originChain,
		ContractAddress: contractAddress,
		OwnerAddress:    ownerAddress,
		Amount:          amount,
		BlockNumber:     blockNumber,
		ReceiptIndex:    receiptIndex,
		ReceiptsRoot:    receiptsRoot,
	}
}

func (msg *MsgBridgeExchange) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Validator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}

	if msg.OriginChain == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "origin chain cannot be empty")
	}

	if msg.ContractAddress == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "contract address cannot be empty")
	}

	if msg.OwnerAddress == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "owner address cannot be empty")
	}

	if msg.BlockNumber == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "block number cannot be empty")
	}

	if msg.ReceiptsRoot == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "receipts root cannot be empty")
	}

	// Validate amount is a valid number
	_, ok := math.NewIntFromString(msg.Amount)
	if !ok {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "invalid amount")
	}

	// Require that OwnerPubKey is not empty
	if msg.OwnerPubKey == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "owner pub key cannot be empty")
	}

	// Validate OwnerPubKey as a hex string (starts with "0x" and has even length)
	if !strings.HasPrefix(msg.OwnerPubKey, "0x") || (len(msg.OwnerPubKey)-2)%2 != 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "invalid owner pub key format (expected hex string starting with 0x)")
	}

	return nil
}
