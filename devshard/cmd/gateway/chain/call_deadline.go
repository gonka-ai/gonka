package chain

import (
	"context"
	"time"
)

type deadlineTransport struct {
	transport Transport
	timeout   time.Duration
}

func (d deadlineTransport) ChainID(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.transport.ChainID(ctx)
}

func (d deadlineTransport) Account(ctx context.Context, address string) (Account, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.transport.Account(ctx, address)
}

func (d deadlineTransport) SpendableBalance(ctx context.Context, address, denom string) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.transport.SpendableBalance(ctx, address, denom)
}

func (d deadlineTransport) Broadcast(ctx context.Context, txBytes []byte) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.transport.Broadcast(ctx, txBytes)
}

func (d deadlineTransport) Tx(ctx context.Context, txHash string) (TxResult, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.transport.Tx(ctx, txHash)
}

func (d deadlineTransport) Escrow(ctx context.Context, escrowID uint64) (EscrowInfo, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.transport.Escrow(ctx, escrowID)
}
