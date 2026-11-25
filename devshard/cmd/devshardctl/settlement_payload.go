package main

import (
	"log"

	"devshard/gatewayprotocol/settlement"
	"devshard/state"
)

func (p *Proxy) settlementPayload() (*state.SettlementPayload, error) {
	finalNonce := p.session.Nonce()
	st := p.sm.SnapshotState()
	signatures, err := settlement.ChooseSignatures(p.session, finalNonce, len(st.Group))
	if err != nil {
		return nil, err
	}
	if signatures.Shortfall != nil {
		log.Printf("devshard_settle_unchecked_signatures escrow=%s nonce=%d error=%q", p.escrowID, finalNonce, signatures.Shortfall.Error())
	}
	return state.BuildSettlement(p.escrowID, st, signatures.BySlot, finalNonce)
}
