package cosmosclient

import (
	"errors"
	"strings"
)

var (
	ErrBuildingUnsignedTx = errors.New("error building unsigned transaction")
	ErrFailedToSignTx     = errors.New("error signing transaction")
	ErrFailedToEncodeTx   = errors.New("error encoding transaction")
	ErrTxTooLarge         = errors.New("tx too large")
	ErrTxNotFound         = errors.New("tx not found")
	ErrDecodingTxHash     = errors.New("error decoding transaction hash")
)

func isTxErrorCritical(err error) bool {
	errString := strings.ToLower(err.Error())
	if errors.Is(err, ErrBuildingUnsignedTx) || errors.Is(err, ErrFailedToSignTx) ||
		errors.Is(err, ErrFailedToEncodeTx) || strings.Contains(errString, ErrTxTooLarge.Error()) {
		return true
	}
	return false
}
