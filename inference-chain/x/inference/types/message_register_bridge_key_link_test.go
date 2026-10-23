package types

import (
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/stretchr/testify/require"
)

func TestBridgeKeyLinkSignText(t *testing.T) {
	got := BridgeKeyLinkSignText("gonka-test", "gonka1abc", "0x02aa")
	require.Equal(t, "Link Ethereum key to Gonka account\nchain-id: gonka-test\ngonka-address: gonka1abc\neth-pubkey: 0x02aa", got)
}

func TestMsgRegisterBridgeKeyLink_ValidateBasic(t *testing.T) {
	gonkaPriv := secp256k1.GenPrivKey()
	ethPriv := secp256k1.GenPrivKey()
	creator := sdk.AccAddress(gonkaPriv.PubKey().Address()).String()
	gonkaPub := CanonicalSecp256k1PubKeyHex(gonkaPriv.PubKey().Bytes())
	ethPub := CanonicalSecp256k1PubKeyHex(ethPriv.PubKey().Bytes())
	sig := make([]byte, 65)

	t.Run("valid", func(t *testing.T) {
		msg := NewMsgRegisterBridgeKeyLink(creator, ethPub, gonkaPub, sig)
		require.NoError(t, msg.ValidateBasic())
	})

	t.Run("bad creator", func(t *testing.T) {
		msg := NewMsgRegisterBridgeKeyLink("not-an-address", ethPub, gonkaPub, sig)
		require.ErrorIs(t, msg.ValidateBasic(), sdkerrors.ErrInvalidAddress)
	})

	t.Run("uppercase creator", func(t *testing.T) {
		msg := NewMsgRegisterBridgeKeyLink(strings.ToUpper(creator), ethPub, gonkaPub, sig)
		require.ErrorIs(t, msg.ValidateBasic(), sdkerrors.ErrInvalidAddress)
	})

	t.Run("overlong public key", func(t *testing.T) {
		msg := NewMsgRegisterBridgeKeyLink(creator, strings.Repeat("A", maxPubKeyEncodingLen+1), gonkaPub, sig)
		require.ErrorIs(t, msg.ValidateBasic(), sdkerrors.ErrInvalidPubKey)
	})

	t.Run("gonka key does not match creator", func(t *testing.T) {
		other := secp256k1.GenPrivKey()
		msg := NewMsgRegisterBridgeKeyLink(creator, ethPub, CanonicalSecp256k1PubKeyHex(other.PubKey().Bytes()), sig)
		require.ErrorIs(t, msg.ValidateBasic(), sdkerrors.ErrInvalidPubKey)
	})

	t.Run("uncompressed eth key", func(t *testing.T) {
		uncompressed := "04" + strings.Repeat("11", 64)
		msg := NewMsgRegisterBridgeKeyLink(creator, uncompressed, gonkaPub, sig)
		require.ErrorIs(t, msg.ValidateBasic(), sdkerrors.ErrInvalidPubKey)
	})

	t.Run("short signature", func(t *testing.T) {
		msg := NewMsgRegisterBridgeKeyLink(creator, ethPub, gonkaPub, []byte{1, 2, 3})
		require.ErrorIs(t, msg.ValidateBasic(), sdkerrors.ErrInvalidRequest)
	})
}
