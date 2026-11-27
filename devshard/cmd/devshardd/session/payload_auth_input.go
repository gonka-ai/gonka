package session

import (
	"strconv"

	"github.com/cosmos/btcutil/bech32"
)

const (
	maxPayloadAddressLen     = 90
	maxPayloadSignatureLen   = 128
	maxPayloadInferenceIDLen = 20
)

func validPayloadAddress(addr string) bool {
	if addr == "" || len(addr) > maxPayloadAddressLen {
		return false
	}
	_, _, err := bech32.Decode(addr, maxPayloadAddressLen)
	return err == nil
}

func validPayloadSignature(sig string) bool {
	return sig != "" && len(sig) <= maxPayloadSignatureLen
}

func validPayloadInferenceID(id string) bool {
	if id == "" || len(id) > maxPayloadInferenceIDLen {
		return false
	}
	_, err := strconv.ParseUint(id, 10, 64)
	return err == nil
}
