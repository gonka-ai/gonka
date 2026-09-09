package bridge

import (
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrNotImplemented      = errors.New("not implemented")
	ErrEscrowNotFound      = errors.New("escrow not found")
	ErrParticipantNotFound = errors.New("participant not found")
	// ErrChainUnavailable means the chain/query path is temporarily unreachable.
	// Lazy session create should map this to HTTP 503 so clients can retry.
	ErrChainUnavailable = errors.New("chain unavailable")
)

// ClassifyQueryError wraps transient query/transport failures as ErrChainUnavailable.
// NotFound stays as ErrEscrowNotFound; other gRPC application errors are returned as-is.
func ClassifyQueryError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrEscrowNotFound) || errors.Is(err, ErrParticipantNotFound) || errors.Is(err, ErrChainUnavailable) {
		return err
	}
	switch status.Code(err) {
	case codes.NotFound:
		return ErrEscrowNotFound
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.ResourceExhausted, codes.Aborted:
		return fmt.Errorf("%w: %w", ErrChainUnavailable, err)
	case codes.InvalidArgument, codes.PermissionDenied, codes.Unauthenticated, codes.FailedPrecondition, codes.AlreadyExists:
		return err
	default:
		// Non-status transport errors (dial failures) and unknown/internal
		// chain blips are treated as retryable.
		return fmt.Errorf("%w: %w", ErrChainUnavailable, err)
	}
}
