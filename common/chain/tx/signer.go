package tx

import (
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
)

// UnorderedSigner signs devshard gateway txs (unordered, sequence 0 in auth info).
type UnorderedSigner interface {
	Address() string
	CompressedPublicKeyBytes() []byte
	Sign(signDoc []byte) ([]byte, error)
}

// KeyringSigner signs ordered txs via a Cosmos keyring record.
type KeyringSigner struct {
	Keyring   keyring.Keyring
	TxConfig  client.TxConfig
	KeyName   string
	Address   string
}
