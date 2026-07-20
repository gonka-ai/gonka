package adminface

// EscrowPublisher emits CometBFT escrow lifecycle events after store mutation.
type EscrowPublisher interface {
	PublishEscrowSettled(id uint64, settler string, totalPayout, fees, remainder uint64) error
}

// EscrowRequest mutates mock-chain escrow records for citest fault injection.
type EscrowRequest struct {
	ID     *uint64 `json:"id,omitempty"`
	Settle bool    `json:"settle,omitempty"`
}
