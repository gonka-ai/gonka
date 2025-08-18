package tx_manager

import (
	"fmt"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

type TransactionError struct {
	TxHash    string
	Code      uint32
	Codespace string
	RawLog    string
}

func (e *TransactionError) Error() string {
	return fmt.Sprintf("transaction %s failed: code=%d, codespace=%s, log=%s", e.TxHash, e.Code, e.Codespace, e.RawLog)
}

func NewTransactionErrorFromResponse(resp *sdk.TxResponse) error {
	if resp == nil {
		return fmt.Errorf("cannot create TransactionError from nil TxResponse")
	}
	if resp.Code == 0 {
		return nil
	}
	return &TransactionError{
		TxHash:    resp.TxHash,
		Code:      resp.Code,
		Codespace: resp.Codespace,
		RawLog:    resp.RawLog,
	}
}

func NewTransactionErrorFromResult(result *ctypes.ResultTx) error {
	if result == nil {
		return fmt.Errorf("cannot create TransactionError from nil ResultTx")
	}
	if result.TxResult.Code == 0 {
		return nil
	}
	return &TransactionError{
		TxHash:    result.Hash.String(),
		Code:      result.TxResult.Code,
		Codespace: result.TxResult.Codespace,
		RawLog:    result.TxResult.Log,
	}
}
