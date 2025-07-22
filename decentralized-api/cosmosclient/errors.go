package cosmosclient

import (
	"errors"
	"strings"
)

var (
	ErrBuildingUnsignedTx      = errors.New("error building unsigned transaction")
	ErrFailedToSignTx          = errors.New("error signing transaction")
	ErrFailedToEncodeTx        = errors.New("error encoding transaction")
	ErrAccountNotFound         = errors.New("key not found")
	ErrTxTooLarge              = errors.New("tx too large")
	ErrTxNotFound              = errors.New("tx not found")
	ErrDecodingTxHash          = errors.New("error decoding transaction hash")
	ErrInvalidAddress          = errors.New("invalid bech32 string")
	ErrAccountSequenceMismatch = errors.New("account sequence mismatch")
)

func isTxErrorCritical(err error) bool {
	errString := strings.ToLower(err.Error())
	if errors.Is(err, ErrBuildingUnsignedTx) || errors.Is(err, ErrFailedToSignTx) ||
		errors.Is(err, ErrFailedToEncodeTx) || strings.Contains(errString, ErrTxTooLarge.Error()) ||
		strings.Contains(errString, ErrAccountNotFound.Error()) || strings.Contains(errString, ErrInvalidAddress.Error()) {
		return true
	}
	return false
}

func isAccountSequenceMismatchError(err error) bool {
	errString := strings.ToLower(err.Error())
	return strings.Contains(errString, ErrAccountSequenceMismatch.Error())
}
