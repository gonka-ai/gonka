package storage

import (
	"errors"

	"subnet/types"
)

// ErrSessionNotFound is returned when a session does not exist in storage.
var ErrSessionNotFound = errors.New("session not found")

// Storage persists subnet session state and diffs.
type Storage interface {
	CreateSession(params CreateSessionParams) error
	MarkSettled(escrowID string) error
	ListActiveSessions() ([]string, error)
	AppendDiff(escrowID string, rec types.DiffRecord) error
	GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error)
	AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error
	GetSignatures(escrowID string, nonce uint64) (map[uint32][]byte, error)
	GetSessionMeta(escrowID string) (*SessionMeta, error)
	MarkFinalized(escrowID string, nonce uint64) error
	LastFinalized(escrowID string) (uint64, error)
	Close() error
}

// CreateSessionParams holds all parameters for creating a new session.
type CreateSessionParams struct {
	EscrowID       string
	CreatorAddr    string
	Config         types.SessionConfig
	Group          []types.SlotAssignment
	InitialBalance uint64
}

// SessionMeta holds session metadata without live state.
type SessionMeta struct {
	EscrowID       string
	CreatorAddr    string
	Config         types.SessionConfig
	Group          []types.SlotAssignment
	InitialBalance uint64
	LatestNonce    uint64
	LastFinalized  uint64
	Status         string // "active", "settled"
}
