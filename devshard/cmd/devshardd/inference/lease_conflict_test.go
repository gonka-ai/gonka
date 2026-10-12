package inference

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	devshardpkg "devshard"
)

// TestLeaseConflict_MatchesSentinel is the compatibility guarantee: callers that
// already branch on ErrValidationAlreadyLeased must keep working when the richer
// error is returned instead, including through an extra wrap.
func TestLeaseConflict_MatchesSentinel(t *testing.T) {
	t.Parallel()
	conflict := &devshardpkg.LeaseConflict{Status: devshardpkg.LeaseStatusPending}

	require.ErrorIs(t, conflict, devshardpkg.ErrValidationAlreadyLeased)
	require.ErrorIs(t, fmt.Errorf("validate: %w", conflict), devshardpkg.ErrValidationAlreadyLeased)

	var target *devshardpkg.LeaseConflict
	require.True(t, errors.As(fmt.Errorf("validate: %w", conflict), &target))
	assert.Equal(t, devshardpkg.LeaseStatusPending, target.Status)
}

func TestLeaseConflict_ErrorReportsObservedRow(t *testing.T) {
	t.Parallel()
	claimedAt := time.Now().Add(-90 * time.Second)
	conflict := &devshardpkg.LeaseConflict{
		Status:    devshardpkg.LeaseStatusSubmitted,
		Owner:     "gonka1owner",
		ClaimedAt: claimedAt,
	}

	msg := conflict.Error()
	assert.Contains(t, msg, "status=submitted")
	assert.Contains(t, msg, "owner=gonka1owner")
	assert.Contains(t, msg, "claimed_at="+claimedAt.UTC().Format(time.RFC3339))
	assert.Contains(t, msg, "age=1m30s")
	assert.NotContains(t, msg, "stale=true")
	assert.True(t, conflict.Observed())
}

func TestLeaseConflict_ErrorReportsStale(t *testing.T) {
	t.Parallel()
	conflict := &devshardpkg.LeaseConflict{
		Status:    devshardpkg.LeaseStatusPending,
		ClaimedAt: time.Now().Add(-time.Hour),
		Stale:     true,
	}
	assert.Contains(t, conflict.Error(), "stale=true")
}

// TestLeaseConflict_ErrorWithoutRow covers the two ways the row cannot be
// reported: it was gone by the time it was read, or the read itself failed. The
// error must still say so rather than asserting a status it never saw.
func TestLeaseConflict_ErrorWithoutRow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		conflict *devshardpkg.LeaseConflict
		want     string
	}{
		{
			name:     "detail carried",
			conflict: &devshardpkg.LeaseConflict{Detail: devshardpkg.LeaseRowAbsentDetail},
			want:     devshardpkg.LeaseRowAbsentDetail,
		},
		{
			name:     "no detail",
			conflict: &devshardpkg.LeaseConflict{},
			want:     "row not read",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			msg := tt.conflict.Error()
			assert.Contains(t, msg, devshardpkg.ErrValidationAlreadyLeased.Error())
			assert.Contains(t, msg, tt.want)
			assert.NotContains(t, msg, "status=")
			assert.False(t, tt.conflict.Observed())
			assert.Equal(t, tt.name == "detail carried", tt.conflict.ReleasedBeforeRead())
		})
	}
}

func TestLeaseConflict_OwnerIsSelf(t *testing.T) {
	t.Parallel()
	conflict := &devshardpkg.LeaseConflict{
		Status:     devshardpkg.LeaseStatusPending,
		Owner:      "gonka1",
		InstanceID: "proc-a",
		Hostname:   "versiond",
	}
	assert.True(t, conflict.OwnerIsSelf("gonka1", "proc-a"))
	assert.False(t, conflict.OwnerIsSelf("gonka1", "proc-b"), "same address, different process")
	assert.False(t, conflict.OwnerIsSelf("gonka2", "proc-a"), "different address")
	assert.False(t, conflict.OwnerIsSelf("gonka1", ""), "blank instance id never matches")

	legacy := &devshardpkg.LeaseConflict{Status: devshardpkg.LeaseStatusPending, Owner: "gonka1"}
	assert.False(t, legacy.OwnerIsSelf("gonka1", "proc-a"))
	assert.False(t, legacy.OwnerIsSelf("gonka1", ""))
}

// TestLeaseConflict_SentinelDoesNotClaimAnotherInstance guards the whole point
// of this error: the acquire cannot tell who holds the row, so neither the
// sentinel nor the typed error may say it belongs to another instance.
func TestLeaseConflict_SentinelDoesNotClaimAnotherInstance(t *testing.T) {
	t.Parallel()
	assert.NotContains(t, devshardpkg.ErrValidationAlreadyLeased.Error(), "another instance")
	assert.NotContains(t, (&devshardpkg.LeaseConflict{Status: devshardpkg.LeaseStatusPending, Owner: "gonka1owner"}).Error(), "another instance")
}
