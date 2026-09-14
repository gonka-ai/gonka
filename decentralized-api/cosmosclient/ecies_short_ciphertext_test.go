package cosmosclient

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/ecies"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	secp256k1v4 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/require"
)

func testKeyringCodec() codec.Codec {
	registry := codectypes.NewInterfaceRegistry()
	registry.RegisterInterface("cosmos.crypto.PubKey", (*cryptotypes.PubKey)(nil))
	registry.RegisterInterface("cosmos.crypto.PrivKey", (*cryptotypes.PrivKey)(nil))
	registry.RegisterImplementations((*cryptotypes.PubKey)(nil), &secp256k1.PubKey{})
	registry.RegisterImplementations((*cryptotypes.PrivKey)(nil), &secp256k1.PrivKey{})
	return codec.NewProtoCodec(registry)
}

func TestDecryptKeyringShortMACValidReturnsError(t *testing.T) {
	kr := keyring.NewInMemory(testKeyringCodec())
	keyName := "short-ecies"
	record, _, err := kr.NewMnemonic(keyName, keyring.English, sdk.FullFundraiserPath, "", hd.Secp256k1)
	require.NoError(t, err)
	pub, err := record.GetPubKey()
	require.NoError(t, err)

	poison, err := encryptUndersizedMACValidForTest(pub.Bytes())
	require.NoError(t, err)
	require.Len(t, poison, 98)

	require.NotPanics(t, func() {
		_, decErr := decryptKeyring(kr, keyName, poison)
		require.Error(t, decErr)
		require.Contains(t, decErr.Error(), "ecies decrypt panic")
	})
}

func encryptUndersizedMACValidForTest(secp256k1PubKeyBytes []byte) ([]byte, error) {
	return encryptUndersizedMACValidForTestWithReader(secp256k1PubKeyBytes, rand.Reader)
}

func encryptUndersizedMACValidForTestWithReader(secp256k1PubKeyBytes []byte, entropy io.Reader) ([]byte, error) {
	pub, err := parseECIESPublicKeyFromCompressedForTest(secp256k1PubKeyBytes)
	if err != nil {
		return nil, err
	}
	params := pub.Params
	if params == nil {
		params = ecies.ParamsFromCurve(pub.Curve)
	}
	if params == nil {
		return nil, fmt.Errorf("no ECIES params for curve")
	}

	R, err := ecies.GenerateKey(entropy, pub.Curve, params)
	if err != nil {
		return nil, err
	}
	z, err := R.GenerateShared(pub, params.KeyLen, params.KeyLen)
	if err != nil {
		return nil, err
	}
	_, km := undersizedDeriveKeysForTest(params.Hash(), z, nil, params.KeyLen)
	em := []byte{0x01}
	tag := undersizedMessageTagForTest(params.Hash, km, em, nil)
	rb := ecies.Marshal(pub.Curve, R.PublicKey.X, R.PublicKey.Y)
	ct := make([]byte, len(rb)+len(em)+len(tag))
	copy(ct, rb)
	copy(ct[len(rb):], em)
	copy(ct[len(rb)+len(em):], tag)
	return ct, nil
}

func parseECIESPublicKeyFromCompressedForTest(secp256k1PubKeyBytes []byte) (*ecies.PublicKey, error) {
	if len(secp256k1PubKeyBytes) != 33 {
		return nil, fmt.Errorf("invalid compressed secp256k1 public key length %d", len(secp256k1PubKeyBytes))
	}
	if secp256k1PubKeyBytes[0] != 0x02 && secp256k1PubKeyBytes[0] != 0x03 {
		return nil, fmt.Errorf("invalid compressed secp256k1 public key prefix 0x%x", secp256k1PubKeyBytes[0])
	}
	pubKey, err := secp256k1v4.ParsePubKey(secp256k1PubKeyBytes)
	if err != nil {
		return nil, err
	}
	return ecies.ImportECDSAPublic(pubKey.ToECDSA()), nil
}

func undersizedDeriveKeysForTest(h hash.Hash, z, s1 []byte, keyLen int) (ke, km []byte) {
	k := undersizedConcatKDFForTest(h, z, s1, 2*keyLen)
	ke = k[:keyLen]
	km = k[keyLen:]
	h.Reset()
	h.Write(km)
	km = h.Sum(km[:0])
	return ke, km
}

func undersizedConcatKDFForTest(h hash.Hash, z, s1 []byte, kdLen int) []byte {
	counterBytes := make([]byte, 4)
	block := h.Size()
	k := make([]byte, 0, kdLen+block)
	for counter := uint32(1); len(k) < kdLen; counter++ {
		binary.BigEndian.PutUint32(counterBytes, counter)
		h.Reset()
		h.Write(counterBytes)
		h.Write(z)
		h.Write(s1)
		k = h.Sum(k)
	}
	return k[:kdLen]
}

func undersizedMessageTagForTest(hashFn func() hash.Hash, km, msg, shared []byte) []byte {
	mac := hmac.New(hashFn, km)
	mac.Write(msg)
	mac.Write(shared)
	return mac.Sum(nil)
}
