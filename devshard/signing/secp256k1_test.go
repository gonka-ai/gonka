package signing

import (
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

func TestCompactSignature_RecoversAndVerifies(t *testing.T) {
	signer, err := GenerateKey()
	require.NoError(t, err)
	msg := []byte("devshard compact")

	full, err := signer.Sign(msg)
	require.NoError(t, err)
	sig, err := CompactSignature(full)
	require.NoError(t, err)
	require.Len(t, sig, CompactSignatureSize)

	var matched []byte
	for _, key := range RecoverCompactPubKeys(msg, sig) {
		require.True(t, VerifyCompact(key, msg, sig), "every recovered key verifies the signature it came from")
		if addr, err := AddressFromPubKey(key); err == nil && addr == signer.Address() {
			matched = key
		}
	}
	require.NotNil(t, matched, "one recovery id gives the signer's key")
	require.False(t, VerifyCompact(matched, []byte("devshard compacT"), sig))

	other, err := signer.Sign([]byte("another message"))
	require.NoError(t, err)
	require.True(t, VerifyCompact(matched, []byte("another message"), other[:CompactSignatureSize]),
		"the signer's key verifies its next signature too")
}

func TestCompactSignature_RejectsBadLengths(t *testing.T) {
	_, err := CompactSignature(make([]byte, 63))
	require.Error(t, err)
	require.Nil(t, RecoverCompactPubKeys([]byte("m"), make([]byte, 65)))
	require.False(t, VerifyCompact(nil, []byte("m"), make([]byte, 65)))
}
