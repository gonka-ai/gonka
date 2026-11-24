package chain

import "errors"

// ErrDevshardBusy means an escrow cannot be settled yet; registry raises it and escrow waits on it.
var ErrDevshardBusy = errors.New("devshard busy")

// SettlementInput is a settle payload with binary fields already decoded; it maps 1:1 onto MsgSettleDevshardEscrow.
type SettlementInput struct {
	EscrowID  uint64
	StateRoot []byte
	Nonce     uint64
	RestHash  []byte
	HostStats []SettlementHostStat
	SlotSigs  []SettlementSlotSig
	Fees      uint64
	Version   string
}

type SettlementHostStat struct {
	SlotID               uint32
	Missed               uint32
	Invalid              uint32
	Cost                 uint64
	RequiredValidations  uint32
	CompletedValidations uint32
}

// SettlementSlotSig is one slot's settlement signature, already decoded.
type SettlementSlotSig struct {
	SlotID    uint64
	Signature []byte
}
