package events

import (
	"context"

	abci "github.com/cometbft/cometbft/abci/types"
)

// Emitted by x/inference/keeper/msg_server_create_devshard_escrow.go.
type DevshardEscrowCreatedEvent struct {
	BlockHeight int64
	EscrowID    string
	Creator     string
	Amount      uint64
	EpochIndex  uint64
}

func (DevshardEscrowCreatedEvent) eventType() string { return "devshard_escrow_created" }
func (DevshardEscrowCreatedEvent) query() string {
	return "tm.event='Tx' AND devshard_escrow_created.escrow_id EXISTS"
}
func (DevshardEscrowCreatedEvent) fromEvent(height int64, ev abci.Event) DevshardEscrowCreatedEvent {
	return DevshardEscrowCreatedEvent{
		BlockHeight: height,
		EscrowID:    attr(ev, "escrow_id"),
		Creator:     attr(ev, "creator"),
		Amount:      parseUint64(attr(ev, "amount")),
		EpochIndex:  parseUint64(attr(ev, "epoch_index")),
	}
}

// Emitted by x/inference/keeper/msg_server_settle_devshard_escrow.go.
type DevshardEscrowSettledEvent struct {
	BlockHeight int64
	EscrowID    string
	Settler     string
	TotalPayout uint64
	Fees        uint64
	Remainder   uint64
}

func (DevshardEscrowSettledEvent) eventType() string { return "devshard_escrow_settled" }
func (DevshardEscrowSettledEvent) query() string {
	return "tm.event='Tx' AND devshard_escrow_settled.escrow_id EXISTS"
}
func (DevshardEscrowSettledEvent) fromEvent(height int64, ev abci.Event) DevshardEscrowSettledEvent {
	return DevshardEscrowSettledEvent{
		BlockHeight: height,
		EscrowID:    attr(ev, "escrow_id"),
		Settler:     attr(ev, "settler"),
		TotalPayout: parseUint64(attr(ev, "total_payout")),
		Fees:        parseUint64(attr(ev, "fees")),
		Remainder:   parseUint64(attr(ev, "remainder")),
	}
}

// NewBlockEvent is emitted for every committed block.
type NewBlockEvent struct {
	BlockHeight int64
}

// DevshardEscrowCreatedHandler is called for each devshard_escrow_created event received.
type DevshardEscrowCreatedHandler func(ctx context.Context, e DevshardEscrowCreatedEvent)

// DevshardEscrowSettledHandler is called for each devshard_escrow_settled event received.
type DevshardEscrowSettledHandler func(ctx context.Context, e DevshardEscrowSettledEvent)

// NewBlockHandler is called for each new committed block.
type NewBlockHandler func(ctx context.Context, e NewBlockEvent)
