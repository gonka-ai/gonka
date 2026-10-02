package payloads

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a requested payload does not exist.
var ErrNotFound = errors.New("payloads: not found")

// ErrAlreadyStored is returned by Store when a payload for the same key was
// stored earlier. The earlier bytes are kept: they are what validators fetch,
// so a caller that hashed different bytes must re-read them before it commits.
var ErrAlreadyStored = errors.New("payloads: already stored")

// ErrSharedPostgresRequired is returned when storage mode needs Postgres but PGHOST is unset.
var ErrSharedPostgresRequired = errors.New(
	"payloads: Postgres required (set PGHOST and PG* env, or set DEVSHARD_STORAGE_MODE=sqlite)",
)

// Storage persists inference prompt/response bytes keyed by escrow, inference, and epoch.
type Storage interface {
	Store(ctx context.Context, escrowId string, inferenceId, epochId uint64, prompt, response []byte) error
	Retrieve(ctx context.Context, escrowId string, inferenceId, epochId uint64) (prompt, response []byte, err error)
	DropEpoch(ctx context.Context, epochId uint64) error
}
