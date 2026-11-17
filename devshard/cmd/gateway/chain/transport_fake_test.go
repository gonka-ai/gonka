package chain

import (
	"context"
	"errors"
	"math"
	"sync"
)

// fakeTransport is an in-memory TxClient transport double.
type fakeTransport struct {
	mu sync.Mutex

	chainID    string
	chainIDErr error

	account    Account
	accountErr error

	balance      uint64
	balanceErr   error
	balanceCalls []balanceCall

	broadcastHash string
	broadcastErr  error
	broadcast     [][]byte

	txs    map[string]TxResult
	txErr  error
	txCall int

	escrow    EscrowInfo
	escrowRaw bool
	escrowErr error

	onTx func(call int)
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{chainID: "gonka-test", txs: map[string]TxResult{}, balance: math.MaxUint64}
}

type balanceCall struct{ address, denom string }

func (f *fakeTransport) ChainID(context.Context) (string, error) {
	if f.chainIDErr != nil {
		return "", f.chainIDErr
	}
	return f.chainID, nil
}

func (f *fakeTransport) Account(context.Context, string) (Account, error) {
	if f.accountErr != nil {
		return Account{}, f.accountErr
	}
	return f.account, nil
}

func (f *fakeTransport) SpendableBalance(_ context.Context, address, denom string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.balanceCalls = append(f.balanceCalls, balanceCall{address: address, denom: denom})
	if f.balanceErr != nil {
		return 0, f.balanceErr
	}
	return f.balance, nil
}

func (f *fakeTransport) Broadcast(_ context.Context, txBytes []byte) (string, error) {
	f.mu.Lock()
	f.broadcast = append(f.broadcast, append([]byte(nil), txBytes...))
	f.mu.Unlock()
	if f.broadcastErr != nil {
		return "", f.broadcastErr
	}
	if f.broadcastHash != "" {
		return f.broadcastHash, nil
	}
	return txHashFromBytes(txBytes), nil
}

func (f *fakeTransport) Tx(_ context.Context, txHash string) (TxResult, bool, error) {
	f.mu.Lock()
	f.txCall++
	call := f.txCall
	f.mu.Unlock()
	if f.onTx != nil {
		f.onTx(call)
	}
	if f.txErr != nil {
		return TxResult{}, false, f.txErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	result, held := f.txs[txHash]
	if !held {
		return TxResult{}, false, nil
	}
	return result, true, nil
}

func (f *fakeTransport) Escrow(context.Context, uint64) (EscrowInfo, bool, error) {
	if f.escrowErr != nil {
		return EscrowInfo{}, false, f.escrowErr
	}
	return f.escrow, f.escrowRaw, nil
}

func (f *fakeTransport) setTx(txHash string, result TxResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.txs[txHash] = result
}

func (f *fakeTransport) broadcasts() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.broadcast...)
}

// escrowCreatedResult is the committed transaction a create waits for.
func escrowCreatedResult(escrowID string) TxResult {
	return TxResult{Events: []TxEvent{{
		Type:       "devshard_escrow_created",
		Attributes: []TxAttribute{{Key: "escrow_id", Value: escrowID}},
	}}}
}

var errTransportRefused = errors.New("transport refused")
