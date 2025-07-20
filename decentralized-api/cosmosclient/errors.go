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
)

func isTxErrorCritical(err error) bool {
	errString := strings.ToLower(err.Error())
	if errors.Is(err, ErrBuildingUnsignedTx) || errors.Is(err, ErrFailedToSignTx) ||
		errors.Is(err, ErrFailedToEncodeTx) || strings.Contains(errString, ErrTxTooLarge.Error()) {
		return true
	}
	return false
}
