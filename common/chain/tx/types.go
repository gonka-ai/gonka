package tx

import (
	"strings"
	"time"
)

const (
	// DefaultChainID is production / mainnet. Override via Config.ChainID or
	// DEVSHARD_CHAIN_ID for other networks (see KnownChainIDs).
	DefaultChainID = "gonka-mainnet"
	// ChainIDTestnet is the public Gonka testnet / devnet chain ID.
	ChainIDTestnet = "gonka-testnet"
	// ChainIDTestenv is the mock-chain ID used by devshard/testenv.
	ChainIDTestenv = "gonka-test"

	DefaultFeeDenom     = "ngonka"
	DefaultFeeAmount    = uint64(1_000_000)
	DefaultGasLimit     = uint64(500_000)
	DefaultPollInterval = 2 * time.Second
	DefaultPollTimeout  = 45 * time.Second
	defaultUnorderedTTL = 9 * time.Minute
)

// KnownChainIDs lists networks with fixed, well-known chain IDs.
var KnownChainIDs = []string{DefaultChainID, ChainIDTestnet, ChainIDTestenv}

// Config holds fee, gas, and tx polling defaults.
type Config struct {
	ChainID      string
	FeeDenom     string
	FeeAmount    uint64
	GasLimit     uint64
	PollInterval time.Duration
	PollTimeout  time.Duration
}

func (c Config) withDefaults() Config {
	out := c
	if strings.TrimSpace(out.ChainID) == "" {
		out.ChainID = DefaultChainID
	}
	if out.FeeDenom == "" {
		out.FeeDenom = DefaultFeeDenom
	}
	if out.FeeAmount == 0 {
		out.FeeAmount = DefaultFeeAmount
	}
	if out.GasLimit == 0 {
		out.GasLimit = DefaultGasLimit
	}
	if out.PollInterval <= 0 {
		out.PollInterval = DefaultPollInterval
	}
	if out.PollTimeout <= 0 {
		out.PollTimeout = DefaultPollTimeout
	}
	return out
}

// CreateDevshardEscrowResult is returned after a successful create tx.
type CreateDevshardEscrowResult struct {
	EscrowID uint64
	TxHash   string
	Creator  string
}

// SettleDevshardEscrowResult is returned after a successful settle tx.
type SettleDevshardEscrowResult struct {
	EscrowID uint64
	TxHash   string
	Settler  string
}

// HostStats mirrors settlement host statistics for MsgSettleDevshardEscrow encoding.
type HostStats struct {
	SlotID               uint32
	Missed               int32
	Invalid              int32
	Cost                 uint64
	RequiredValidations  int32
	CompletedValidations int32
}

// SlotSignature is one slot signature on a settlement tx.
type SlotSignature struct {
	SlotID    uint32
	Signature []byte
}

// SettleParams carries gateway settlement fields for unordered settle encoding.
type SettleParams struct {
	EscrowID                    uint64
	StateRoot                   []byte
	Nonce                       uint64
	RestHash                    []byte
	HostStats                   []HostStats
	Signatures                  []SlotSignature
	Fees                        uint64
	StateRootAndProtocolVersion []byte
}

type chainAccount struct {
	AccountNumber uint64
	Sequence      uint64
}
