// Package settlement holds the settle message both gateways post, the slot order of its lists, the signatures it posts and the settlement root. See README.md.
package settlement

// Message is a settle payload with binary fields already decoded; with the settler it maps onto MsgSettleDevshardEscrow field for field, except that HostStat carries Validated and Finished, which settlementwire does not marshal.
type Message struct {
	EscrowID       uint64
	StateRoot      []byte
	Nonce          uint64
	RestHash       []byte
	HostStats      []HostStat
	SlotSignatures []SlotSignature
	Fees           uint64
	Version        string
}

// HostStat is one slot's settled counters.
type HostStat struct {
	SlotID               uint32
	Missed               uint32
	Invalid              uint32
	Cost                 uint64
	RequiredValidations  uint32
	CompletedValidations uint32
	Validated            uint32
	Finished             uint32
}

// SlotSignature is one slot's settlement signature, already decoded.
type SlotSignature struct {
	SlotID    uint32
	Signature []byte
}
