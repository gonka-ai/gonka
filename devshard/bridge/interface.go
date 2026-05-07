package bridge

// MainnetBridge defines the interface between the devshard and mainnet.
// Phase 1: interface only, no implementation.
type MainnetBridge interface {
	// Notifications: mainnet -> devshard
	OnEscrowCreated(escrow EscrowInfo) error
	OnSettlementProposed(escrowID uint64, stateRoot []byte, nonce uint64) error
	OnSettlementFinalized(escrowID uint64) error

	// Queries: devshard -> mainnet
	GetEscrow(escrowID uint64) (*EscrowInfo, error)
	GetHostInfo(address string) (*HostInfo, error)
	VerifyWarmKey(warmAddress, validatorAddress string) (bool, error)

	// Actions: devshard -> mainnet
	SubmitDisputeState(escrowID uint64, stateRoot []byte, nonce uint64, sigs map[uint32][]byte) error
}

type EscrowInfo struct {
	EscrowID       uint64
	Amount         uint64
	CreatorAddress string
	AppHash        []byte
	Slots          []string // host addresses, len == DevshardGroupSize
	TokenPrice     uint64
}

type HostInfo struct {
	Address string
	URL     string
}
