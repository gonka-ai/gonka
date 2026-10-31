package chain

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
	SlotID               uint64
	Missed               uint64
	Invalid              uint64
	Cost                 uint64
	RequiredValidations  uint64
	CompletedValidations uint64
}

// SettlementSlotSig is one slot's settlement signature, already decoded.
type SettlementSlotSig struct {
	SlotID    uint64
	Signature []byte
}
