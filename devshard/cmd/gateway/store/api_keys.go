package store

import (
	"errors"
	"time"
)

// ErrAPIKeyIDTaken reports an insert whose ID an earlier key already holds, revoked or not.
var ErrAPIKeyIDTaken = errors.New("an API key with this ID already exists")

// APIKeyRecord is one client key as stored; RevokedAt is the zero time while the key is active.
type APIKeyRecord struct {
	ID        string
	Key       string
	Label     string
	CreatedAt time.Time
	RevokedAt time.Time
}

// Revoked reports whether the key was revoked.
func (record APIKeyRecord) Revoked() bool {
	return !record.RevokedAt.IsZero()
}
