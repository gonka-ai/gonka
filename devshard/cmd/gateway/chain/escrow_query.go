package chain

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// EscrowInfo is the escrow as the gateway reads it. See README.md, "The gRPC transport".
type EscrowInfo struct {
	EscrowID   string
	Balance    uint64
	EpochIndex uint64
	Settled    bool
}

// GetEscrow reports found=false only when the chain says the escrow is absent. See README.md, "The gRPC transport".
func (c *TxClient) GetEscrow(ctx context.Context, escrowID string) (EscrowInfo, bool, error) {
	numericID, err := strconv.ParseUint(strings.TrimSpace(escrowID), 10, 64)
	if err != nil {
		return EscrowInfo{}, false, fmt.Errorf("escrow id %q is not a number: %w", escrowID, err)
	}
	return c.transport.Escrow(ctx, numericID)
}
