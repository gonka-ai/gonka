package testutil

import (
	"crypto/sha256"
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

const (
	Creator    = "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2"
	Requester  = "gonka1rdyphrqxe9l5hkp7uxcruch64sh337jasqsntr"
	Executor   = "gonka1pda35dczayfhy2udffky7wzset9tpkpatzaksd"
	Validator  = "gonka13779rkgy6ke7cdj8f097pdvx34uvrlcqq8nq2w"
	Executor2  = "gonka1xxczezuqw0pe67xag5s3zgyrzh4w3zyermjgs9"
	Validator2 = "gonka1l47s7lufh2v0s6t4r9kg3gz8whwg3w4hfvqm55"
)

// Bech32Addr returns a valid bech32-encoded account address with the given HRP.
func Bech32Addr(seed int) string {
	hrp := sdk.GetConfig().GetBech32AccountAddrPrefix()
	// Deterministic private key from seed
	h := sha256.Sum256([]byte(fmt.Sprintf("addr-seed-%d", seed)))
	priv := secp256k1.PrivKey{Key: h[:]}
	pub := priv.PubKey()

	// Convert to AccAddress (20 bytes)
	addr := sdk.AccAddress(pub.Address())

	// Encode with desired HRP/prefix
	bech, err := sdk.Bech32ifyAddressBytes(hrp, addr)
	if err != nil {
		panic(err)
	}
	return bech
}
