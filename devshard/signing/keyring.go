package signing

import (
	"fmt"

	sdkcrypto "github.com/cosmos/cosmos-sdk/crypto"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// NewSignerFromKeyring extracts the secp256k1 private key from a cosmos keyring
// and returns a devshard signer that produces gonka bech32 addresses.
func NewSignerFromKeyring(kr keyring.Keyring, uid string) (*Secp256k1Signer, error) {
	armor, err := kr.ExportPrivKeyArmor(uid, "")
	if err != nil {
		return nil, fmt.Errorf("export priv key: %w", err)
	}

	privKey, _, err := sdkcrypto.UnarmorDecryptPrivKey(armor, "")
	if err != nil {
		return nil, fmt.Errorf("unarmor priv key: %w", err)
	}

	ecdsaKey, err := ethcrypto.ToECDSA(privKey.Bytes())
	if err != nil {
		return nil, fmt.Errorf("to ecdsa: %w", err)
	}

	return NewSecp256k1Signer(ecdsaKey)
}
