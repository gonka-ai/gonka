package signing

import (
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

func newTestCodec() codec.Codec {
	registry := codectypes.NewInterfaceRegistry()
	registry.RegisterInterface("cosmos.crypto.PubKey", (*cryptotypes.PubKey)(nil))
	registry.RegisterInterface("cosmos.crypto.PrivKey", (*cryptotypes.PrivKey)(nil))
	registry.RegisterImplementations((*cryptotypes.PubKey)(nil), &secp256k1.PubKey{})
	registry.RegisterImplementations((*cryptotypes.PrivKey)(nil), &secp256k1.PrivKey{})
	return codec.NewProtoCodec(registry)
}

func newTestKeyring(t *testing.T, uid string) keyring.Keyring {
	t.Helper()
	kr := keyring.NewInMemory(newTestCodec())
	_, _, err := kr.NewMnemonic(uid, keyring.English, sdk.FullFundraiserPath, "", hd.Secp256k1)
	require.NoError(t, err)
	return kr
}

func TestNewSignerFromKeyring_Address(t *testing.T) {
	kr := newTestKeyring(t, "test")

	signer, err := NewSignerFromKeyring(kr, "test")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(signer.Address(), "gonka1"))
}

func TestNewSignerFromKeyring_SignRecoverRoundTrip(t *testing.T) {
	kr := newTestKeyring(t, "test")

	signer, err := NewSignerFromKeyring(kr, "test")
	require.NoError(t, err)

	msg := []byte("hello devshard payload")
	sig, err := signer.Sign(msg)
	require.NoError(t, err)

	recovered, err := NewSecp256k1Verifier().RecoverAddress(msg, sig)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), recovered)
}

func TestNewSignerFromKeyring_UnknownUID(t *testing.T) {
	kr := keyring.NewInMemory(newTestCodec())

	_, err := NewSignerFromKeyring(kr, "nonexistent")
	require.Error(t, err)
	require.Contains(t, err.Error(), "export priv key")
}

func TestNewSignerFromKeyring_DeterministicAddress(t *testing.T) {
	// Same key → same address across two signer instances.
	kr := newTestKeyring(t, "test")

	s1, err := NewSignerFromKeyring(kr, "test")
	require.NoError(t, err)
	s2, err := NewSignerFromKeyring(kr, "test")
	require.NoError(t, err)

	require.Equal(t, s1.Address(), s2.Address())
}
