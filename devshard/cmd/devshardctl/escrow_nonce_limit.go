package main

import "devshard/types"

// nonceInFlightMargin keeps requests already routed to an escrow below the nonce its hosts stop accepting work at.
const nonceInFlightMargin uint64 = 200

// escrowNonceLimit is the nonce at which the gateway stops routing to an escrow: an in-flight margin short of the hosts' active cap, or the old chain default until the chain value is known.
func escrowNonceLimit(chainMaxNonce uint32, slots uint32) uint64 {
	if chainMaxNonce == 0 {
		return nonceDeactivationLimit
	}
	limit := types.MaxActiveNonce(chainMaxNonce, int(slots))
	if limit > nonceInFlightMargin {
		limit -= nonceInFlightMargin
	}
	return limit
}

// chainMaxNonce is the chain's devshard max_nonce, zero while it is not yet known.
func (g *Gateway) chainMaxNonce() uint32 {
	if g.maxNonce == nil {
		return 0
	}
	return g.maxNonce.MaxNonce()
}
