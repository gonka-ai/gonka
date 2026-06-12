package bridge

import "devshard/types"

// MainnetBridge defines the interface between the devshard and mainnet.
// Phase 1: interface only, no implementation.
type MainnetBridge interface {
	// Notifications: mainnet -> devshard
	OnEscrowCreated(escrow EscrowInfo) error
	OnSettlementProposed(escrowID string, stateRoot []byte, nonce uint64) error
	OnSettlementFinalized(escrowID string) error

	// Queries: devshard -> mainnet
	GetEscrow(escrowID string) (*EscrowInfo, error)
	GetHostInfo(address string) (*HostInfo, error)
	GetValidationThreshold(epochID uint64, modelID string) (*Decimal, error)
	VerifyWarmKey(warmAddress, validatorAddress string) (bool, error)

	// Actions: devshard -> mainnet
	SubmitDisputeState(escrowID string, stateRoot []byte, nonce uint64, sigs map[uint32][]byte) error
}

// SessionBindParamsBridge supplies lane-B governance fields read from chain
// Params at session bind time (see devshard/docs/params-dataflow.md).
type SessionBindParamsBridge interface {
	GetSessionBindParams() (types.LiveSessionBindParams, error)
}

type EscrowInfo struct {
	EscrowID       string
	Amount         uint64
	CreatorAddress string
	AppHash        []byte
	Slots          []string // host addresses, len == DevshardGroupSize
	TokenPrice                uint64
	CreateDevshardFee         uint64
	FeePerNonce               uint64
	InferenceSealGraceNonces  uint32
	InferenceSealGraceSeconds uint32
	AutoSealEveryNNonces      uint32
	// EpochID is the chain epoch_index recorded on the on-chain DevshardEscrow.
	// Storage uses it as the partition/pruning key.
	EpochID uint64
}

type HostInfo struct {
	Address string
	URL     string
}

type Decimal struct {
	Value    int64 `json:"value,string"`
	Exponent int32 `json:"exponent"`
}
