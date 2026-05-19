package storage

import (
	"errors"

	"devshard/types"
)

// ErrSessionNotFound is returned when a session does not exist in storage.
var ErrSessionNotFound = errors.New("session not found")

// ErrSessionEpochConflict is returned when local storage finds the same
// escrow_id mapped to more than one epoch. Mainnet pins this mapping on the
// DevshardEscrow, so local storage must not choose a different epoch silently.
var ErrSessionEpochConflict = errors.New("session epoch conflict")

// ErrSessionVersionConflict is returned when an escrow already belongs to a
// different devshard binary version tag. Operators can run multiple
// devshardd binaries against the same Postgres database, so storage pins one
// binary tag per escrow to prevent a binary that ships a different
// state-root composition from attaching to live state mid-session.
var ErrSessionVersionConflict = errors.New("session version conflict")

// ErrSnapshotNotFound is returned when no snapshot exists for a session.
var ErrSnapshotNotFound = errors.New("snapshot not found")

// ErrEpochPruned is returned when a managed store is asked to create a session
// in an epoch that has already passed the local retention horizon.
var ErrEpochPruned = errors.New("epoch already pruned")

// Storage persists devshard session state and diffs.
//
// The store is partitioned by EpochID. PruneEpoch drops everything that
// belongs to the given epoch in O(1) without touching other partitions.
// All session-keyed methods (AppendDiff, GetDiffs, AddSignature, ...) are
// resolved internally by an escrow_id -> epoch_id index built from
// CreateSession and from a startup scan, so callers do not pass epoch_id
// on every operation.
type Storage interface {
	CreateSession(params CreateSessionParams) error
	MarkSettled(escrowID string) error
	ListActiveSessions() ([]ActiveSession, error)
	AppendDiff(escrowID string, rec types.DiffRecord) error
	GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error)
	AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error
	GetSignatures(escrowID string, nonce uint64) (map[uint32][]byte, error)
	GetSessionMeta(escrowID string) (*SessionMeta, error)
	MarkFinalized(escrowID string, nonce uint64) error
	LastFinalized(escrowID string) (uint64, error)
	SaveSnapshot(escrowID string, nonce uint64, data []byte) error
	LoadSnapshot(escrowID string) (nonce uint64, data []byte, err error)
	InsertSealedInference(escrowID string, row InferenceRow) error
	GetSealedInference(escrowID string, inferenceID uint64) (InferenceRow, bool, error)
	DeleteSealedInferences(escrowID string) error
	PruneEpoch(epochID uint64) error
	Close() error
}

// CreateSessionParams holds all parameters for creating a new session.
type CreateSessionParams struct {
	EscrowID       string
	EpochID        uint64
	Version        string
	CreatorAddr    string
	Config         types.SessionConfig
	Group          []types.SlotAssignment
	InitialBalance uint64
}

// SessionMeta holds session metadata without live state.
type SessionMeta struct {
	EscrowID       string
	EpochID        uint64
	Version        string
	CreatorAddr    string
	Config         types.SessionConfig
	Group          []types.SlotAssignment
	InitialBalance uint64
	LatestNonce    uint64
	LastFinalized  uint64
	Status         string // "active", "settled"
}

// ActiveSession is the lightweight tuple returned by ListActiveSessions.
// EpochID lets callers (HostManager.RecoverSessions in particular) route
// follow-up reads to the right partition without an extra meta lookup.
type ActiveSession struct {
	EscrowID string
	EpochID  uint64
}

// InferenceRow is the durable sealed-inference marker used by Phase 0 RAM
// pruning. It only records that an inference id was sealed and the nonce at
// which the seal happened. Cold-path validation (late MsgValidation /
// MsgValidationVote on an evicted record) reads the full per-record state
// from the in-memory committed-entries map, which is itself rebuilt from the
// host snapshot and the diff log on recovery; this row is purely the durable
// "this id was sealed" signal.
type InferenceRow struct {
	InferenceID uint64
	SealedNonce uint64
}
