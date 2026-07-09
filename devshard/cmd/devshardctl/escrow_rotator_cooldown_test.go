package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The create-failure latch is a bounded cooldown, not an epoch-permanent flag: it clears after rotationCreateFailureCooldown within the same epoch, and the expired entry is deleted (before the fix a single transient 503 stranded routable capacity for a whole epoch).
func TestRotationCreateLatchExpiresAfterCooldown(t *testing.T) {
	g := &Gateway{rotationFailures: make(map[string]time.Time)}
	const (
		model = "Qwen/Test"
		role  = rotationRoleTemp
		epoch = uint64(10)
	)
	key := g.rotationFailureKey(model, role, epoch)

	// Fresh failure -> suppressed inside the cooldown window.
	g.recordRotationCreateFailure(model, role, epoch)
	require.True(t, g.rotationCreateFailed(model, role, epoch), "suppressed right after a 503")

	// Back-date the failure past the cooldown (same epoch) -> must un-suppress.
	g.rotationFailures[key] = time.Now().Add(-rotationCreateFailureCooldown - time.Second)
	require.False(t, g.rotationCreateFailed(model, role, epoch), "latch must expire within the same epoch")
	_, still := g.rotationFailures[key]
	require.False(t, still, "expired entry is deleted, not left to leak")
}

// Drives the real gate (ensureRotationEscrows) through fail -> suppressed-in-cooldown -> retry-after-cooldown within one epoch; time advances by back-dating the cached failure instant, so the test is deterministic (no sleep, no clock injection).
func TestEnsureRotationEscrowsRetriesAfterCooldown(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		EscrowRotation: EscrowRotationSettings{
			Enabled:           true,
			SettlementEnabled: false, // rotation without settlement — the reported case
			Models: []EscrowRotationModelSettings{{
				ModelID:       "Qwen/Test",
				TempCount:     4,
				TargetCount:   8,
				Amount:        1000,
				PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY",
			}},
		},
	}.WithTuningDefaults()
	require.NoError(t, store.Initialize(settings, nil))

	model := normalizedEscrowRotationModels(settings)[0]
	const (
		role   = rotationRoleTemp
		epoch  = uint64(10)
		target = 4
	)

	oldCreate := gatewayCreateRotationEscrow
	t.Cleanup(func() { gatewayCreateRotationEscrow = oldCreate })
	createAttempts := 0
	failCreates := true
	gatewayCreateRotationEscrow = func(*Gateway, context.Context, GatewaySettings, EscrowRotationModelSettings, string, uint64) (*CreateDevshardEscrowResult, error) {
		createAttempts++
		if failCreates {
			return nil, fmt.Errorf("503 Service Unavailable")
		}
		return &CreateDevshardEscrowResult{EscrowID: uint64(createAttempts)}, nil
	}

	g := &Gateway{store: store, rotationFailures: make(map[string]time.Time)}

	// Tick 1: the RPC returns 503 -> create fails, latch set.
	_, err = g.ensureRotationEscrows(context.Background(), settings, model, role, epoch, target)
	require.Error(t, err)
	require.Equal(t, 1, createAttempts, "first tick attempts create once, then latches on the 503")

	// Tick 2, inside the cooldown: suppressed, no chain call made.
	_, err = g.ensureRotationEscrows(context.Background(), settings, model, role, epoch, target)
	require.ErrorIs(t, err, errEscrowRotationCreateSuppressed)
	require.Equal(t, 1, createAttempts, "inside the cooldown the create is suppressed, no retry")

	// Cooldown elapses (same epoch) and the RPC recovers -> rotation must retry.
	g.rotationFailures[g.rotationFailureKey(model.ModelID, role, epoch)] = time.Now().Add(-rotationCreateFailureCooldown - time.Second)
	failCreates = false
	res, err := g.ensureRotationEscrows(context.Background(), settings, model, role, epoch, target)
	require.NoError(t, err)
	require.Greater(t, createAttempts, 1, "after the cooldown, rotation retries within the same epoch (the fix)")
	require.Equal(t, target, res.CreatedCount, "recovery fills the target escrow count")
}

// The admin reset clears the whole cache immediately: it returns the count cleared, empties the map, un-suppresses on the next check, and rejects non-POST.
func TestResetRotationCreateFailures(t *testing.T) {
	g := &Gateway{rotationFailures: make(map[string]time.Time)}
	g.recordRotationCreateFailure("Qwen/Test", rotationRoleTemp, 10)
	g.recordRotationCreateFailure("Qwen/Test", rotationRoleRegular, 10)
	require.True(t, g.rotationCreateFailed("Qwen/Test", rotationRoleTemp, 10), "suppressed before reset")

	require.Equal(t, 2, g.resetRotationCreateFailures(), "reset returns the number of cleared entries")
	require.Empty(t, g.rotationFailures, "reset empties the cache")
	require.False(t, g.rotationCreateFailed("Qwen/Test", rotationRoleTemp, 10), "reset un-suppresses immediately")

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/escrow-rotation/reset-failures", nil)
	rec := httptest.NewRecorder()
	g.handleAdminResetRotationFailures(rec, req)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, "endpoint is POST-only")
}
