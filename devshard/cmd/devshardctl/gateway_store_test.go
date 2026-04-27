package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayStoreInitializeAndLoadState(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1234,
		MaxConcurrentRequests:   5,
		MaxInputTokensInFlight:  999,
	}.WithTuningDefaults()
	devshards := []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{
			ID:            "12",
			PrivateKeyHex: "secret",
			Model:         "Qwen/Test",
			StoragePath:   "/root/.devshardctl/escrow-12/state.db",
		},
		Active: true,
	}}

	require.NoError(t, store.Initialize(settings, devshards))

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, settings, state.Settings)
	require.Len(t, state.Devshards, 1)
	require.Equal(t, "12", state.Devshards[0].ID)
	require.True(t, state.Devshards[0].Active)
	require.Equal(t, "/root/.devshardctl/escrow-12/state.db", state.Devshards[0].StoragePath)
}

func TestAdminAuthMiddlewareRequiresAdminKey(t *testing.T) {
	handler := adminAuthMiddleware("adminkey", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/state", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/v1/admin/state", nil)
	req.Header.Set("Authorization", "Bearer adminkey")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestGatewayStoreUpdateSettings(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, nil))

	require.NoError(t, store.UpdateSettings(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 2000,
		MaxConcurrentRequests:   5,
		MaxInputTokensInFlight:  500,
		ParticipantThrottle: ParticipantThrottleSettings{
			RequestBurst:                   42,
			RecoveryPerMinute:              7,
			HTTPQuarantineMS:               1100,
			TransportFailureQuarantineMS:   1200,
			EmptyStreamQuarantineMS:        1300,
			StalledWinnerQuarantineMS:      1400,
			EmptyStreamQuarantineThreshold: 2,
		},
		Redundancy: RedundancySettings{
			ReceiptTimeoutMS:             1500,
			FirstTokenTimeoutFloorMS:     1600,
			PerInputTokenFirstTokenLagMS: 17,
			InterChunkStallTimeoutMS:     1800,
			NonStreamResponseFloorMS:     1900,
			PerInputTokenResponseLagMS:   20,
			SecondaryWaitAfterWinnerMS:   2100,
			ParallelAdvantageThreshold:   0.4,
			UnresponsiveThreshold:        0.8,
		},
	}))

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 2000, state.Settings.DefaultRequestMaxTokens)
	require.EqualValues(t, 5, state.Settings.MaxConcurrentRequests)
	require.EqualValues(t, 500, state.Settings.MaxInputTokensInFlight)
	require.EqualValues(t, 42, state.Settings.ParticipantThrottle.RequestBurst)
	require.EqualValues(t, 1200, state.Settings.ParticipantThrottle.TransportFailureQuarantineMS)
	require.EqualValues(t, 2, state.Settings.ParticipantThrottle.EmptyStreamQuarantineThreshold)
	require.EqualValues(t, 1500, state.Settings.Redundancy.ReceiptTimeoutMS)
	require.EqualValues(t, 17, state.Settings.Redundancy.PerInputTokenFirstTokenLagMS)
	require.Equal(t, 0.4, state.Settings.Redundancy.ParallelAdvantageThreshold)
}
