package devshard

// MaxNonceProvider supplies the current chain devshard_escrow_params.max_nonce.
// Hosts read it on each diff to reserve headroom for finalization and settlement.
type MaxNonceProvider interface {
	MaxNonce() uint32
}

// StaticMaxNonce is a fixed limit for tests.
type StaticMaxNonce uint32

func (s StaticMaxNonce) MaxNonce() uint32 { return uint32(s) }
