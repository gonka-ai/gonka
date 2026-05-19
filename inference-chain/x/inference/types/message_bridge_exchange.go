package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"math/big"
	"regexp"
)

var _ sdk.Msg = &MsgBridgeExchange{}

func NewMsgBridgeExchange(validator string, originChain string, contractAddress string, ownerAddress string, ownerPubKey string, amount string, blockNumber string, receiptIndex string, receiptsRoot string) *MsgBridgeExchange {
	return &MsgBridgeExchange{
		Validator:       validator,
		OriginChain:     originChain,
		ContractAddress: contractAddress,
		OwnerAddress:    ownerAddress,
		OwnerPubKey:     ownerPubKey,
		Amount:          amount,
		BlockNumber:     blockNumber,
		ReceiptIndex:    receiptIndex,
		ReceiptsRoot:    receiptsRoot,
	}
}

var reDigits = regexp.MustCompile(`^[0-9]+$`) //nolint:forbidigo // init code

func (msg *MsgBridgeExchange) ValidateBasic() error {
	// validator bech32 signer
	if _, err := sdk.AccAddressFromBech32(msg.Validator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid validator address (%s)", err)
	}
	// required non-empty strings
	if len(msg.OriginChain) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "originChain is required")
	}
	if len(msg.ContractAddress) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "contractAddress is required")
	}
	if len(msg.OwnerAddress) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "ownerAddress is required")
	}
	if _, err := sdk.AccAddressFromBech32(msg.OwnerAddress); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid ownerAddress (%s)", err)
	}
	if len(msg.OwnerPubKey) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "ownerPubKey is required")
	}
	if len(msg.Amount) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "amount is required")
	}
	if len(msg.BlockNumber) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "blockNumber is required")
	}
	if len(msg.ReceiptIndex) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "receiptIndex is required")
	}
	if len(msg.ReceiptsRoot) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "receiptsRoot is required")
	}
	// numeric strings: blockNumber and receiptIndex must be unsigned integers without leading zeros
	if !reDigits.MatchString(msg.BlockNumber) || !decimalStringWithoutLeadingZeros(msg.BlockNumber) {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "blockNumber must be a base-10 unsigned integer string without leading zeros")
	}
	if !reDigits.MatchString(msg.ReceiptIndex) || !decimalStringWithoutLeadingZeros(msg.ReceiptIndex) {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "receiptIndex must be a base-10 unsigned integer string without leading zeros")
	}
	if !reDigits.MatchString(msg.Amount) || !decimalStringWithoutLeadingZeros(msg.Amount) {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "amount must be a base-10 unsigned integer string without leading zeros")
	}
	if !isValidReceiptsRootHex(msg.ReceiptsRoot) {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "receiptsRoot must be 0x-prefixed 32-byte hex")
	}
	if bi, ok := new(big.Int).SetString(msg.Amount, 10); !ok || bi.Sign() <= 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "amount must be > 0")
	}
	return nil
}
