package payloadstorage

import (
	"context"
	"errors"
)

var ErrNotFound = errors.New("payload not found")

type PayloadStorage interface {
	Store(ctx context.Context, inferenceId string, epochId uint64, promptPayload, responsePayload []byte) error
	Retrieve(ctx context.Context, inferenceId string, epochId uint64) (promptPayload, responsePayload []byte, err error)
	PruneEpoch(ctx context.Context, epochId uint64) error
}
