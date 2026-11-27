package store

import (
	"errors"
	"time"

	"devshard/cmd/gateway/internal/tokensource"
)

// ErrDevshardNotFound is returned by updates/deletes that match no row.
var ErrDevshardNotFound = errors.New("devshard not found")

// DevshardRecord is one row of the registry; private keys are never stored, only the env var name that holds one.
type DevshardRecord struct {
	EscrowID          string `json:"escrow_id"`
	PrivateKeyEnv     string `json:"private_key_env"`
	Model             string `json:"model"`
	Active            bool   `json:"active"`
	RotationRole      Role   `json:"rotation_role"`
	RotationEpoch     int64  `json:"rotation_epoch"`
	SettlementPending bool   `json:"settlement_pending"`
	SettleTxHash      string `json:"settle_tx_hash"`
	RoutePrefix       string `json:"route_prefix"`
	ChainEpoch        uint64 `json:"chain_epoch"`
	Amount            uint64 `json:"amount"`
	GoneFromChain     bool   `json:"gone_from_chain"`
}

// Parked reports a row settlement owns: waiting for its settle, or with one in flight.
func (r DevshardRecord) Parked() bool {
	return r.SettlementPending || r.SettleTxHash != ""
}

// Commitment is the durable intent for one escrow create, written before the tx broadcasts. See README.md, "The commitment row".
type Commitment struct {
	TxHash        string
	Model         string
	Role          Role
	PrivateKeyEnv string
	Epoch         uint64
	BlockHeight   int64
	CreatedAt     time.Time
}

// RequestRecord carries no monetary cost: the race outcome that writes it knows only token counts.
type RequestRecord struct {
	RequestID          string
	EscrowID           string
	Model              string
	Outcome            RequestOutcome
	Decision           string
	Stream             bool
	WinnerNonce        uint64
	WinnerParticipant  string
	WinnerHost         string
	WinnerHostIdx      int
	Attempts           int
	InputTokens        uint64
	InputTokensSource  tokensource.Source
	PromptTokens       int64
	WinnerOutputTokens int64
	TotalOutputTokens  int64
	EscrowMissing      bool
	BalanceExhausted   bool
	StartedAt          time.Time
	CompletedAt        time.Time
	FirstTokenMS       int64
	DurationMS         int64
	RecordedAt         time.Time
}

// Retention rejects a zero on either axis, so an unbounded ledger cannot be configured.
type Retention struct {
	MaxAge  time.Duration
	MaxRows int64
}

// LedgerStats counts what the ledger writer stored, shed, lost to the database and failed to sweep.
type LedgerStats struct {
	Written     int64
	Dropped     int64
	Failed      int64
	SweepFailed int64
}

// OwedVote is one refusal vote a race owes, with the prompt the hosts check that vote against. See README.md, "Owed refusal votes".
type OwedVote struct {
	EscrowID    string
	Nonce       uint64
	StartedAt   time.Time
	RequestID   string
	Participant string
	Model       string
	Prompt      []byte
}

// OwedVoteStats counts the saves and deletes the writer shed or the database refused.
type OwedVoteStats struct {
	Dropped int64
	Failed  int64
}

// ModelFilterRow is one operator filter setting; the store keeps its knobs as the JSON the API decoded.
type ModelFilterRow struct {
	Model       string
	Preset      string
	FiltersJSON string
	UpdatedAt   string
}

// RotationStatus is the latest outcome of one (Model, Role) rotation stage, kept for admin-debug visibility only.
type RotationStatus struct {
	Model       string    `json:"model"`
	Role        Role      `json:"role"`
	Stage       string    `json:"stage"`
	Epoch       uint64    `json:"epoch"`
	Completed   bool      `json:"completed"`
	CreateError string    `json:"create_error"`
	UpdatedAt   time.Time `json:"updated_at"`
}
