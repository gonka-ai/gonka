package signing

import (
	"crypto/sha256"
	"fmt"
	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/hkdf"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSign_RecoverAddress(t *testing.T) {
	signer, err := GenerateKey()
	require.NoError(t, err)

	verifier := NewSecp256k1Verifier()
	msg := []byte("hello devshard")

	sig, err := signer.Sign(msg)
	require.NoError(t, err)

	recovered, err := verifier.RecoverAddress(msg, sig)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), recovered)
}

func TestSign_DifferentKeys(t *testing.T) {
	signer1, err := GenerateKey()
	require.NoError(t, err)
	signer2, err := GenerateKey()
	require.NoError(t, err)

	verifier := NewSecp256k1Verifier()
	msg := []byte("test message")

	sig1, err := signer1.Sign(msg)
	require.NoError(t, err)
	sig2, err := signer2.Sign(msg)
	require.NoError(t, err)

	addr1, err := verifier.RecoverAddress(msg, sig1)
	require.NoError(t, err)
	addr2, err := verifier.RecoverAddress(msg, sig2)
	require.NoError(t, err)

	require.NotEqual(t, addr1, addr2)
	require.Equal(t, signer1.Address(), addr1)
	require.Equal(t, signer2.Address(), addr2)
}

func TestVerify_TamperedMessage(t *testing.T) {
	signer, err := GenerateKey()
	require.NoError(t, err)

	verifier := NewSecp256k1Verifier()
	msg := []byte("original message")

	sig, err := signer.Sign(msg)
	require.NoError(t, err)

	tampered := []byte("tampered message")
	recovered, err := verifier.RecoverAddress(tampered, sig)
	require.NoError(t, err)
	require.NotEqual(t, signer.Address(), recovered)
}

func TestAddress_GonkaBech32(t *testing.T) {
	signer, err := GenerateKey()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(signer.Address(), "gonka1"))
}

func TestAddressFromPubKey_Compressed(t *testing.T) {
	signer, err := GenerateKey()
	require.NoError(t, err)

	// Uncompressed 65-byte key
	uncompressed := signer.PublicKeyBytes()
	addr, err := AddressFromPubKey(uncompressed)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), addr)
}

func TestAddressFromPubKey_InvalidLength(t *testing.T) {
	_, err := AddressFromPubKey([]byte{1, 2, 3})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid pubkey length")
}

func TestPeerSessionSecretKeepsLegacyDerivationAndScopesSharedKeys(t *testing.T) {
	signer, err := GenerateKey()
	require.NoError(t, err)
	host, version := signer.Address(), "v6"
	old := make([]byte, 32)
	_, err = io.ReadFull(hkdf.New(sha256.New, crypto.FromECDSA(signer.key), nil, []byte(fmt.Sprintf("devshard/peer-rpc-session/v1|%s|%s|%d", host, version, 1))), old)
	require.NoError(t, err)
	got, err := signer.DerivePeerSessionKey(host, version, 1)
	require.NoError(t, err)
	require.Equal(t, old, got)
	seed := make([]byte, 32)
	seed[0] = 42
	key, err := DerivePeerSessionKeyFromSecret(seed, host, "v7", 1)
	require.NoError(t, err)
	for _, scope := range []struct {
		host, version string
		id            byte
	}{{host, "v6", 1}, {"other", "v7", 1}, {host, "v7", 2}} {
		other, err := DerivePeerSessionKeyFromSecret(seed, scope.host, scope.version, scope.id)
		require.NoError(t, err)
		require.NotEqual(t, key, other)
	}
	_, err = DerivePeerSessionKeyFromSecret(nil, host, "v7", 1)
	require.Error(t, err)
}
