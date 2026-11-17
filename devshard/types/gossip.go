package types

// GossipSig is a host signature for one nonce, produced when applying
// recovered diffs. Gossip relays these to peers.
type GossipSig struct {
	Nonce     uint64
	StateHash []byte
	Sig       []byte
	SlotID    uint32
}
