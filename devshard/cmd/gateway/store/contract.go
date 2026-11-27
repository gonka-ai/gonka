// Package store is the contract of the gateway's control-plane state: the interfaces callers depend on, the domain types and the writers every backend shares. The SQLite backend is store/sqlite, the Postgres one store/postgres, and store/storetest runs one suite against both. See README.md.
package store

import (
	"context"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/user"
)

// Devshards is the escrow registry. See README.md, "The devshard registry".
type Devshards interface {
	UpsertDevshard(ctx context.Context, record DevshardRecord) error
	ListDevshards(ctx context.Context) ([]DevshardRecord, error)
	Devshard(ctx context.Context, escrowID string) (DevshardRecord, bool, error)
	SetDevshardActive(ctx context.Context, escrowID string, active bool) error
	SetDevshardSettlementPending(ctx context.Context, escrowID string, pending bool) error
	SetDevshardChainFacts(ctx context.Context, escrowID string, chainEpoch, amount uint64) error
	MarkDevshardGoneFromChain(ctx context.Context, escrowID string) error
	ParkForSettlement(ctx context.Context, escrowID string) error
	ParkForSettlementIfActive(ctx context.Context, escrowID string) (bool, error)
	DeleteDevshard(ctx context.Context, escrowID string) error
	DevshardSettleTxHash(ctx context.Context, escrowID string) (hash string, broadcastAt time.Time, err error)
	SetDevshardRotationRole(ctx context.Context, escrowID string, role Role) error
	SetDevshardSettleTxHash(ctx context.Context, escrowID, txHash string) error
	ActivateDevshardUnlessParked(ctx context.Context, escrowID string) (bool, error)
	UpsertDevshardUnlessParked(ctx context.Context, record DevshardRecord) (bool, error)
}

// Commitments holds the intent of an escrow create; SaveCommitment returns nil only once the row survives a machine crash. See README.md, "The commitment row".
type Commitments interface {
	SaveCommitment(ctx context.Context, commitment Commitment) error
	LoadCommitments(ctx context.Context) ([]Commitment, error)
	DeleteCommitment(ctx context.Context, txHash string) error
}

// Overrides persists the admin's runtime overrides.
type Overrides interface {
	LoadOverrides(ctx context.Context) (config.Overrides, error)
	SaveOverrides(ctx context.Context, overrides config.Overrides) error
}

// ModelFilters persists the operator's per-model filter settings.
type ModelFilters interface {
	UpsertModelFilter(ctx context.Context, row ModelFilterRow) error
	DeleteModelFilter(ctx context.Context, model string) error
	ListModelFilters(ctx context.Context) ([]ModelFilterRow, error)
}

// RotationStatuses persists the latest outcome of each rotation stage.
type RotationStatuses interface {
	SaveRotationStatus(ctx context.Context, status RotationStatus) error
	LoadRotationStatuses(ctx context.Context) ([]RotationStatus, error)
}

// SuspiciousHosts persists the participants pinned as never-to-be-trusted.
type SuspiciousHosts interface {
	AddSuspiciousHost(ctx context.Context, participantKey string) error
	RemoveSuspiciousHost(ctx context.Context, participantKey string) error
	ListSuspiciousHosts(ctx context.Context) ([]string, error)
}

// APIKeys persists the client API keys; a revoked key keeps its row, so its ID is never reused. See README.md, "The API keys".
type APIKeys interface {
	InsertAPIKey(ctx context.Context, record APIKeyRecord) error
	RevokeAPIKey(ctx context.Context, id string, revokedAt time.Time) error
	APIKey(ctx context.Context, id string) (APIKeyRecord, bool, error)
	ListAPIKeys(ctx context.Context) ([]APIKeyRecord, error)
	ActiveAPIKeys(ctx context.Context) ([]string, error)
}

// Completions is the inference completion journal; a nonce is saved before it is sent. See README.md, "The completion journal".
type Completions interface {
	user.InferenceCompletionStore
	HasInferenceCompletion(escrowID string, nonce uint64) (bool, error)
}

// RequestRows is the persistence under the accounting Ledger.
type RequestRows interface {
	InsertRequest(ctx context.Context, record RequestRecord) error
	FindRequest(ctx context.Context, requestID string) (RequestRecord, bool, error)
	DeleteRequestsBefore(ctx context.Context, cutoff time.Time) error
	DeleteRequestsBeyond(ctx context.Context, keepRows int64) error
}

// OwedVoteRows is the persistence under OwedVotes.
type OwedVoteRows interface {
	SaveOwedVote(ctx context.Context, vote OwedVote) error
	DeleteOwedVote(ctx context.Context, escrowID string, nonce uint64) error
	LoadOwedVotes(ctx context.Context) ([]OwedVote, error)
}

// Backend is what a database implementation provides: every area above, the retry policy its errors call for, and Close.
type Backend interface {
	Devshards
	Commitments
	Overrides
	ModelFilters
	RotationStatuses
	SuspiciousHosts
	APIKeys
	Completions
	RequestRows
	OwedVoteRows
	WithRetry(ctx context.Context, operation func() error) error
	Close() error
}

// Store is a Backend plus the asynchronous writers over it; its Close drains them before the connection goes.
type Store interface {
	Backend
	NewLedger(retention Retention, now func() time.Time) (*Ledger, error)
	NewOwedVotes() (*OwedVotes, error)
}
