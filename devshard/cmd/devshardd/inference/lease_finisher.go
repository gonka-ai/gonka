package inference

import (
	"context"
	"errors"
	"fmt"
	"time"

	devshardpkg "devshard"
	"devshard/storage"
)

// StorageLeaseFinisher finishes RetryLoop-held leases from Host.validateAsync.
type StorageLeaseFinisher struct {
	leases       storage.LeaseStore
	instanceAddr string
	leaseTTL     time.Duration
}

// NewStorageLeaseFinisher wraps a LeaseStore for Option B LeaseHeld finish.
func NewStorageLeaseFinisher(leases storage.LeaseStore, instanceAddr string, leaseTTL time.Duration) *StorageLeaseFinisher {
	if leaseTTL <= 0 {
		leaseTTL = 30 * time.Minute
	}
	return &StorageLeaseFinisher{
		leases:       leases,
		instanceAddr: instanceAddr,
		leaseTTL:     leaseTTL,
	}
}

func (f *StorageLeaseFinisher) OwnsPendingLease(ctx context.Context, escrowID string, inferenceID uint64) (bool, error) {
	return f.leases.OwnsPendingLease(ctx, escrowID, inferenceID, f.instanceAddr)
}

func (f *StorageLeaseFinisher) MarkSubmitted(ctx context.Context, escrowID string, inferenceID uint64) error {
	err := f.leases.SetResult(ctx, escrowID, inferenceID, storage.LeaseStatusSubmitted, f.instanceAddr)
	if errors.Is(err, storage.ErrLeaseNotOwned) {
		return fmt.Errorf("%w: %v", devshardpkg.ErrValidationLeaseAbandoned, err)
	}
	return err
}

func (f *StorageLeaseFinisher) LeaseTTL() time.Duration {
	return f.leaseTTL
}

var _ devshardpkg.LeaseFinisher = (*StorageLeaseFinisher)(nil)
