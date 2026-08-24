// Package cosmos signs as the account behind a gonka address and reads that address back out of a
// signature, so a host can tell who asked while holding nothing of theirs
package cosmos

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdkcrypto "github.com/cosmos/cosmos-sdk/crypto"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	cosmossecp "github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"

	"trainshard/internal/domain/shared/vo"
)

const prefix = "gonka"

type Key struct {
	private *secp256k1.PrivateKey
	address vo.Address
}

func FromKeyring(dir, backend, password, name string) (*Key, error) {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)

	ring, err := keyring.New("inferenced", backend, dir, strings.NewReader(password), codec.NewProtoCodec(registry))
	if err != nil {
		return nil, fmt.Errorf("keyring %q: %w", dir, err)
	}
	armored, err := ring.ExportPrivKeyArmor(name, "")
	if err != nil {
		return nil, fmt.Errorf("key %q: %w", name, err)
	}
	private, _, err := sdkcrypto.UnarmorDecryptPrivKey(armored, "")
	if err != nil {
		return nil, fmt.Errorf("key %q: %w", name, err)
	}
	return fromBytes(private.Bytes())
}

func FromHex(raw string) (*Key, error) {
	decoded, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(raw), "0x"))
	if err != nil {
		return nil, fmt.Errorf("private key: %w", err)
	}
	return fromBytes(decoded)
}

func (k *Key) Address() vo.Address { return k.address }

// Account hands the key over in the shape the chain's own transaction signing takes
func (k *Key) Account() cryptotypes.PrivKey {
	return &cosmossecp.PrivKey{Key: k.private.Serialize()}
}

func (k *Key) Sign(payload []byte) []byte {
	digest := sha256.Sum256(payload)
	return ecdsa.SignCompact(k.private, digest[:], true)
}

func (k *Key) Attest(_ context.Context, payload []byte) ([]byte, error) {
	return k.Sign(payload), nil
}

func (k *Key) Recover(payload, signature []byte) (vo.Address, error) {
	return Recover(payload, signature)
}

// Recover names the account that signed: the signature carries the key that made it, so reading a
// caller's address takes nothing that belongs to the caller
func Recover(payload, signature []byte) (vo.Address, error) {
	digest := sha256.Sum256(payload)
	public, _, err := ecdsa.RecoverCompact(signature, digest[:])
	if err != nil {
		return "", fmt.Errorf("signature: %w", err)
	}
	return toAddress(public)
}

func fromBytes(raw []byte) (*Key, error) {
	if len(raw) != secp256k1.PrivKeyBytesLen {
		return nil, fmt.Errorf("a private key is %d bytes, got %d", secp256k1.PrivKeyBytesLen, len(raw))
	}
	private := secp256k1.PrivKeyFromBytes(raw)
	if private.Key.IsZero() {
		return nil, fmt.Errorf("a private key cannot be zero")
	}
	address, err := toAddress(private.PubKey())
	if err != nil {
		return nil, err
	}
	return &Key{private: private, address: address}, nil
}

func toAddress(public *secp256k1.PublicKey) (vo.Address, error) {
	account := &cosmossecp.PubKey{Key: public.SerializeCompressed()}
	encoded, err := bech32.ConvertAndEncode(prefix, account.Address())
	if err != nil {
		return "", err
	}
	return vo.ParseAddress(encoded)
}
