package utils

import (
	"encoding/base64"
	"encoding/hex"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

func PubKeyToString(pubKey cryptotypes.PubKey) string {
	if pubKey == nil {
		return ""
	}
	return PubKeyBytesToString(pubKey.Bytes())
}

func PubKeyBytesToString(pubKeyBytes []byte) string {
	if pubKeyBytes == nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(pubKeyBytes)
}

func PubKeyToHexString(pubKey cryptotypes.PubKey) string {
	return hex.EncodeToString(pubKey.Bytes())
}

func OperatorAddressToAccAddress(operatorAddress string) (string, error) {
	valAddr, err := sdk.ValAddressFromBech32(operatorAddress)
	if err != nil {
		return "", err
	}

	accAddr := sdk.AccAddress(valAddr)
	return accAddr.String(), nil
}
