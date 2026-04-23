package gossip

import (
	"context"

	"subnet/types"
)

// PeerClient sends gossip messages to a single peer.
type PeerClient interface {
	GossipNonce(ctx context.Context, nonce uint64, stateHash, stateSig []byte, slotID uint32) error
	GossipTxs(ctx context.Context, txs []*types.SubnetTx) error
}

// GossipSig carries a host's signature for a specific nonce, ready for gossip.
type GossipSig struct {
	Nonce     uint64
	StateHash []byte
	Sig       []byte
	SlotID    uint32
}

// DiffFetcher retrieves diffs from a peer (backed by HTTPClient).
type DiffFetcher interface {
	GetDiffs(ctx context.Context, fromNonce, toNonce uint64) ([]types.Diff, error)
}

// StateUpdater applies recovered diffs and signs them (backed by Host).
type StateUpdater interface {
	ApplyRecoveredDiffs(ctx context.Context, diffs []types.Diff) ([]GossipSig, error)
}

// SigAccumulator receives gossip signatures for nonces that are already applied locally.
type SigAccumulator interface {
	AccumulateGossipSig(nonce uint64, stateHash, sig []byte, senderSlot uint32) error
}

// MempoolSink receives transactions from gossip peers.
type MempoolSink interface {
	AddTx(tx *types.SubnetTx)
}

