package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgRegisterBridgeKeyLink{}

func NewMsgRegisterBridgeKeyLink(creator, ethPubKey, gonkaPubKey string, ethSignature []byte, nonce uint64) *MsgRegisterBridgeKeyLink {
	return &MsgRegisterBridgeKeyLink{
		Creator:      creator,
		EthPubKey:    ethPubKey,
		GonkaPubKey:  gonkaPubKey,
		EthSignature: ethSignature,
		Nonce:        nonce,
	}
}

func (msg *MsgRegisterBridgeKeyLink) ValidateBasic() error {
	creator, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	if msg.Creator != creator.String() {
		return errorsmod.Wrap(sdkerrors.ErrInvalidAddress, "creator must be canonical bech32")
	}
	ethPub, err := ParseCompressedSecp256k1PubKey(msg.EthPubKey)
	if err != nil {
		return errorsmod.Wrapf(err, "eth_pub_key")
	}
	gonkaPub, err := ParseCompressedSecp256k1PubKey(msg.GonkaPubKey)
	if err != nil {
		return errorsmod.Wrapf(err, "gonka_pub_key")
	}
	derived, err := AddressFromCompressedPubKey(gonkaPub)
	if err != nil {
		return errorsmod.Wrapf(err, "gonka_pub_key")
	}
	if !derived.Equals(creator) {
		return errorsmod.Wrap(sdkerrors.ErrInvalidPubKey, "gonka_pub_key does not match creator")
	}
	if len(msg.EthSignature) != 65 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "eth_signature must be 65 bytes (r || s || v)")
	}
	if msg.Nonce == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "nonce must be greater than zero")
	}
	// ethPub is checked for length above; reference it so a future edit that
	// drops the parse is a compile failure if the variable becomes unused.
	if len(ethPub) != 33 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidPubKey, "eth_pub_key must be 33 bytes")
	}
	return nil
}
