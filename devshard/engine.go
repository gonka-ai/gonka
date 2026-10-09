package devshard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrValidationAlreadyLeased is returned when a validation lease row already
// exists for an inference, so this attempt did not acquire it.
//
// It does not mean another instance holds the lease. The acquire is an upsert
// that reports only that some row was in the way; the row may be this
// instance's own residue from before a restart, a row a sibling process holds,
// or a completed row kept to block a duplicate submit. Callers that need to
// tell those apart must inspect a LeaseConflict.
var ErrValidationAlreadyLeased = errors.New("validation lease already held")

// Validation lease statuses as carried on LeaseConflict. They mirror the
// storage-layer values without importing the storage package.
const (
	LeaseStatusPending   = "pending"
	LeaseStatusSubmitted = "submitted"
	LeaseStatusSkipped   = "skipped"
)

// LeaseConflict reports what was observed about the lease row that refused an
// acquire. It wraps ErrValidationAlreadyLeased, so existing errors.Is callers
// are unaffected.
//
// Every field is diagnostic. The row is read after the failed acquire rather
// than atomically with it, so it may already have moved on, and Status is
// empty when the row could not be read at all.
type LeaseConflict struct {
	// Status is pending, submitted, or skipped; empty when unknown.
	Status string
	// Owner is the instance_address on the row. It is the participant signer
	// address, shared by every instance of one participant. InstanceID is the
	// process that wrote the row; empty on a row written before process
	// identity existed. Hostname is the container that wrote it. Empty when
	// unknown.
	Owner      string
	InstanceID string
	Hostname   string
	// ClaimedAt is when the row was last claimed; zero when unknown.
	ClaimedAt time.Time
	// Stale reports that ClaimedAt is older than the lease TTL, meaning nothing
	// has reclaimed the row yet. Only meaningful for a pending status.
	Stale bool
	// Detail explains an empty Status: the row was gone by the time it was
	// read, or the read itself failed.
	Detail string
}

// LeaseRowAbsentDetail is Detail when Acquire lost to a row that was gone
// before the follow-up read. The inference can be picked again immediately.
const LeaseRowAbsentDetail = "row absent when read; already released"

// ReleasedBeforeRead reports that the conflicting row was already gone, so a
// retry does not need to wait out the validation cooldown.
func (e *LeaseConflict) ReleasedBeforeRead() bool {
	return e != nil && !e.Observed() && e.Detail == LeaseRowAbsentDetail
}

// Observed reports whether the conflicting row was actually read.
func (e *LeaseConflict) Observed() bool {
	return e != nil && e.Status != ""
}

// OwnerIsSelf reports whether this conflict's row was written by the process
// identified by address and instanceID. Hostname is not consulted. An empty
// instance id never matches, including a legacy row whose instance id is blank.
func (e *LeaseConflict) OwnerIsSelf(address, instanceID string) bool {
	if e == nil || instanceID == "" || e.InstanceID == "" {
		return false
	}
	return e.Owner == address && e.InstanceID == instanceID
}

func (e *LeaseConflict) Error() string {
	if e == nil {
		return ErrValidationAlreadyLeased.Error()
	}
	var b strings.Builder
	b.WriteString(ErrValidationAlreadyLeased.Error())
	if !e.Observed() {
		detail := e.Detail
		if detail == "" {
			detail = "row not read"
		}
		return b.String() + ": " + detail
	}
	fmt.Fprintf(&b, ": status=%s", e.Status)
	if e.Owner != "" {
		fmt.Fprintf(&b, " owner=%s", e.Owner)
	}
	if e.InstanceID != "" {
		fmt.Fprintf(&b, " instance_id=%s", e.InstanceID)
	}
	if e.Hostname != "" {
		fmt.Fprintf(&b, " hostname=%s", e.Hostname)
	}
	if !e.ClaimedAt.IsZero() {
		fmt.Fprintf(&b, " claimed_at=%s age=%s",
			e.ClaimedAt.UTC().Format(time.RFC3339), time.Since(e.ClaimedAt).Truncate(time.Second))
	}
	if e.Stale {
		b.WriteString(" stale=true")
	}
	return b.String()
}

func (e *LeaseConflict) Unwrap() error { return ErrValidationAlreadyLeased }

// ErrValidationLeaseAbandoned is returned when this instance must not submit or
// complete a lease: local acquire TTL exceeded, or the pending lease is no
// longer owned (stolen / completed).
var ErrValidationLeaseAbandoned = errors.New("validation lease abandoned")

// ErrValidationLeaseTTLExceeded is returned with ErrValidationLeaseAbandoned
// when the refusal is because this instance was too slow, not because someone
// else owns the work. Callers that release the lease should also back off.
var ErrValidationLeaseTTLExceeded = errors.New("elapsed since acquire exceeds lease TTL")

// ErrValidationSkipped signals that a validation attempt was deliberately
// abandoned without producing a MsgValidation or MsgValidationVote.
// The canonical trigger is the executor returning 404 for the payload
// (the payload has already been pruned). Callers should treat this as a
// quiet no-op rather than a validation failure.
var ErrValidationSkipped = errors.New("devshard validation skipped")

// InferenceEngine executes inference on an ML node.
// Implemented by dapi using existing broker + completionapi.
type InferenceEngine interface {
	Execute(ctx context.Context, req ExecuteRequest) (*ExecuteResult, error)
}

// ValidationEngine re-executes inference and compares logits.
// Implemented by dapi using existing broker + completionapi.
type ValidationEngine interface {
	Validate(ctx context.Context, req ValidateRequest) (*ValidateResult, error)
}

// ValidationCompletionRecorder can be implemented by validation engines that
// need to gate async MsgValidation submission and persist lease completion.
type ValidationCompletionRecorder interface {
	// AllowValidationSubmit must be called before publishing MsgValidation.
	// ErrValidationLeaseAbandoned means skip submit and do not mark submitted.
	AllowValidationSubmit(ctx context.Context, escrowID string, inferenceID uint64) error
	MarkValidationSubmitted(ctx context.Context, escrowID string, inferenceID uint64) error
	// ReleaseValidationLease frees a pending lease this instance owns so the
	// inference can be re-picked. No-op if this instance has no remembered acquire.
	ReleaseValidationLease(ctx context.Context, escrowID string, inferenceID uint64) error
}
