package utils

import (
	"encoding/base64"
	"encoding/hex"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
)

func PubKeyToString(pubKey cryptotypes.PubKey) string {
	return base64.StdEncoding.EncodeToString(pubKey.Bytes())
}

func PubKeyToHexString(pubKey cryptotypes.PubKey) string {
	return hex.EncodeToString(pubKey.Bytes())
}
