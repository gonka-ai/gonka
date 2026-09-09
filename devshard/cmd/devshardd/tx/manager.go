package tx

import (
	chaintx "common/chain/tx"

	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"google.golang.org/grpc"
)

// Manager signs and broadcasts devshard chain transactions over gRPC.
type Manager = chaintx.Manager

// New creates a Manager for ordered dispute txs via keyring.
func New(conn grpc.ClientConnInterface, kr keyring.Keyring, address, signerKeyName, chainID string) (*Manager, error) {
	return chaintx.NewWithKeyring(conn, kr, address, signerKeyName, chainID, chaintx.Config{})
}

// SubmitDisputeState is implemented on *Manager via common/chain/tx.
