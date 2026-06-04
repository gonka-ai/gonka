package main

import (
	"context"
	"encoding/json"
	"fmt"
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
			StoragePath:   "/root/.devshardctl/escrow-12",
		},
		Active:        true,
		RotationRole:  rotationRoleRegular,
		RotationEpoch: 7,
	}}

	require.NoError(t, store.Initialize(settings, devshards))

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, settings, state.Settings)
	require.Len(t, state.Devshards, 1)
	require.Equal(t, "12", state.Devshards[0].ID)
	require.True(t, state.Devshards[0].Active)
	require.Equal(t, "/root/.devshardctl/escrow-12", state.Devshards[0].StoragePath)
	require.Equal(t, rotationRoleRegular, state.Devshards[0].RotationRole)
	require.EqualValues(t, 7, state.Devshards[0].RotationEpoch)
	require.False(t, state.Settings.Disabled.Enabled)
	require.Equal(t, defaultGatewayDisabledMessage, state.Settings.Disabled.Message)
	require.Empty(t, state.Settings.Disabled.NewURL)
}

func TestAdminAuthMiddlewareRequiresAdminKey(t *testing.T) {
	for _, path := range []string{
		"/v1/admin/state",
		"/v1/finalize",
		"/devshard/12/v1/finalize",
		"/v1/state",
		"/devshard/12/v1/state",
		"/v1/debug/state",
		"/devshard/12/v1/debug/signatures/collect",
	} {
		handler := adminAuthMiddleware("adminkey", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))

		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code)

		req = httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer adminkey")
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNoContent, rec.Code)
	}
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
		ModelLimits: []GatewayModelLimitSettings{
			{ModelID: "Qwen/Test", MaxConcurrentRequests: 7, MaxInputTokensInFlight: 700, DefaultRequestMaxTokens: 3072, RequestMaxTokensCap: 4096, AccessMode: "api_key"},
			{ModelID: "Kimi/Rotate", MaxConcurrentRequests: 3, MaxInputTokensInFlight: 300, AccessMode: "admin_only", AccessMessage: "Kimi temporarily unavailable"},
		},
		Disabled: GatewayDisabledSettings{
			Enabled: true,
			Message: "please use ... base url",
			NewURL:  "https://.../v1/chat/completions",
		},
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
			ReceiptTimeoutMS:              1500,
			FirstTokenTimeoutFloorMS:      1600,
			PerInputTokenFirstTokenLagMS:  17,
			InterChunkStallTimeoutMS:      1800,
			StreamingAttemptHardTimeoutMS: 1810,
			NonStreamResponseFloorMS:      1900,
			NonStreamNoContentTimeoutMS:   2200,
			NonStreamMaxAttemptWaitMS:     2600,
			PerInputTokenResponseLagMS:    20,
			SecondaryWaitAfterWinnerMS:    2100,
			ParallelAdvantageThreshold:    0.4,
			UnresponsiveThreshold:         0.8,
		},
		EscrowRotation: EscrowRotationSettings{
			Enabled:           true,
			SettlementEnabled: true,
			PrePoCBlocks:      123,
			Models: []EscrowRotationModelSettings{{
				ModelID:       "Kimi/Rotate",
				TempCount:     2,
				TargetCount:   6,
				Amount:        555,
				PrivateKeyEnv: "KIMI_ROTATION_KEY",
			}},
		},
	}))

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 2000, state.Settings.DefaultRequestMaxTokens)
	require.EqualValues(t, 5, state.Settings.MaxConcurrentRequests)
	require.EqualValues(t, 500, state.Settings.MaxInputTokensInFlight)
	require.Equal(t, []GatewayModelLimitSettings{
		{ModelID: "Qwen/Test", MaxConcurrentRequests: 7, MaxInputTokensInFlight: 700, DefaultRequestMaxTokens: 3072, RequestMaxTokensCap: 4096, AccessMode: "api_key"},
		{ModelID: "Kimi/Rotate", MaxConcurrentRequests: 3, MaxInputTokensInFlight: 300, AccessMode: "admin_only", AccessMessage: "Kimi temporarily unavailable"},
	}, state.Settings.ModelLimits)
	require.True(t, state.Settings.Disabled.Enabled)
	require.Equal(t, "please use ... base url", state.Settings.Disabled.Message)
	require.Equal(t, "https://.../v1/chat/completions", state.Settings.Disabled.NewURL)
	require.EqualValues(t, 42, state.Settings.ParticipantThrottle.RequestBurst)
	require.EqualValues(t, 1200, state.Settings.ParticipantThrottle.TransportFailureQuarantineMS)
	require.EqualValues(t, 2, state.Settings.ParticipantThrottle.EmptyStreamQuarantineThreshold)
	require.EqualValues(t, 1500, state.Settings.Redundancy.ReceiptTimeoutMS)
	require.EqualValues(t, 17, state.Settings.Redundancy.PerInputTokenFirstTokenLagMS)
	require.EqualValues(t, 1810, state.Settings.Redundancy.StreamingAttemptHardTimeoutMS)
	require.EqualValues(t, 2200, state.Settings.Redundancy.NonStreamNoContentTimeoutMS)
	require.EqualValues(t, 2600, state.Settings.Redundancy.NonStreamMaxAttemptWaitMS)
	require.Equal(t, 0.4, state.Settings.Redundancy.ParallelAdvantageThreshold)
	require.True(t, state.Settings.EscrowRotation.Enabled)
	require.True(t, state.Settings.EscrowRotation.SettlementEnabled)
	require.EqualValues(t, 123, state.Settings.EscrowRotation.PrePoCBlocks)
	require.Equal(t, []EscrowRotationModelSettings{{
		ModelID:       "Kimi/Rotate",
		TempCount:     2,
		TargetCount:   6,
		Amount:        555,
		PrivateKeyEnv: "KIMI_ROTATION_KEY",
	}}, state.Settings.EscrowRotation.Models)
}

func TestGatewayStoreLoadsLegacyModelAccessIntoModelLimits(t *testing.T) {
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
		ModelLimits: []GatewayModelLimitSettings{{
			ModelID:               "Qwen/Test",
			MaxConcurrentRequests: 7,
		}},
	}, nil))

	legacyAccess, err := json.Marshal([]GatewayModelAccessSettings{{
		ModelID: "Qwen/Test",
		Enabled: false,
		Message: "Qwen temporarily unavailable",
	}})
	require.NoError(t, err)
	_, err = store.db.Exec(`UPDATE gateway_settings SET model_access_json = ? WHERE id = 1`, string(legacyAccess))
	require.NoError(t, err)

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []GatewayModelLimitSettings{{
		ModelID:               "Qwen/Test",
		MaxConcurrentRequests: 7,
		AccessMode:            "admin_only",
		AccessMessage:         "Qwen temporarily unavailable",
	}}, state.Settings.ModelLimits)
}

func TestValidateGatewaySettingsRequiresRotationModels(t *testing.T) {
	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
	}.WithTuningDefaults()
	settings.EscrowRotation.Enabled = true

	err := validateGatewaySettings(settings)
	require.Error(t, err)
	require.Contains(t, err.Error(), "models")

	settings.EscrowRotation.Models = []EscrowRotationModelSettings{{
		ModelID:       "Kimi/Test",
		TempCount:     8,
		TargetCount:   16,
		PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY",
	}}
	err = validateGatewaySettings(settings)
	require.Error(t, err)
	require.Contains(t, err.Error(), "amount")

	settings.EscrowRotation.Models[0].Amount = 1000
	require.NoError(t, validateGatewaySettings(settings))

	settings.EscrowRotation.Models = []EscrowRotationModelSettings{{
		ModelID:       "Kimi/Test",
		TempCount:     8,
		TargetCount:   16,
		Amount:        1000,
		PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY",
	}, {
		ModelID:       "Kimi/Test",
		TempCount:     8,
		TargetCount:   16,
		Amount:        1000,
		PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY",
	}}
	err = validateGatewaySettings(settings.WithTuningDefaults())
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate model_id")
}

func TestEscrowRotationPreparePromotesRegularEscrowsOnTempCreateFailure(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		EscrowRotation: EscrowRotationSettings{
			Enabled:           true,
			SettlementEnabled: true,
			Models: []EscrowRotationModelSettings{{
				ModelID:       "Qwen/Test",
				TempCount:     8,
				TargetCount:   16,
				Amount:        1000,
				PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY",
			}},
		},
	}.WithTuningDefaults()
	require.NoError(t, store.Initialize(settings, []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "secret", Model: "Qwen/Test"},
		Active:        true,
		RotationRole:  rotationRoleRegular,
		RotationEpoch: 9,
	}, {
		RuntimeConfig: RuntimeConfig{ID: "13", PrivateKeyHex: "secret", Model: "Kimi/Test"},
		Active:        true,
		RotationRole:  rotationRoleRegular,
		RotationEpoch: 9,
	}}))

	oldCreate := gatewayCreateRotationEscrow
	oldSettle := gatewaySettleDevshardOnChain
	createAttempts := 0
	settleAttempts := 0
	gatewayCreateRotationEscrow = func(*Gateway, context.Context, GatewaySettings, EscrowRotationModelSettings, string, uint64) (*CreateDevshardEscrowResult, error) {
		createAttempts++
		return nil, fmt.Errorf("epoch already has 100 escrows")
	}
	gatewaySettleDevshardOnChain = func(*Gateway, context.Context, string, adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		settleAttempts++
		return nil, nil
	}
	t.Cleanup(func() {
		gatewayCreateRotationEscrow = oldCreate
		gatewaySettleDevshardOnChain = oldSettle
	})

	g := &Gateway{store: store, rotationFailures: make(map[string]struct{})}
	g.prepareBridgeEscrows(ChainPhaseSnapshot{EpochIndex: 10}, settings)
	g.prepareBridgeEscrows(ChainPhaseSnapshot{EpochIndex: 10}, settings)

	require.Equal(t, 1, createAttempts)
	require.Equal(t, 0, settleAttempts)

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	byID := gatewayDevshardsByID(state.Devshards)
	require.Equal(t, rotationRoleTemp, byID["12"].RotationRole)
	require.EqualValues(t, 10, byID["12"].RotationEpoch)
	require.Equal(t, rotationRoleRegular, byID["13"].RotationRole)
}

func TestEscrowRotationFinishDoesNotSettleTempWhenRegularCreateFails(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		EscrowRotation: EscrowRotationSettings{
			Enabled:           true,
			SettlementEnabled: true,
			Models: []EscrowRotationModelSettings{{
				ModelID:       "Qwen/Test",
				TempCount:     1,
				TargetCount:   16,
				Amount:        1000,
				PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY",
			}},
		},
	}.WithTuningDefaults()
	require.NoError(t, store.Initialize(settings, []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "secret", Model: "Qwen/Test"},
		Active:        true,
		RotationRole:  rotationRoleTemp,
		RotationEpoch: 10,
	}}))

	oldCreate := gatewayCreateRotationEscrow
	oldSettle := gatewaySettleDevshardOnChain
	createAttempts := 0
	settleAttempts := 0
	gatewayCreateRotationEscrow = func(*Gateway, context.Context, GatewaySettings, EscrowRotationModelSettings, string, uint64) (*CreateDevshardEscrowResult, error) {
		createAttempts++
		return nil, fmt.Errorf("insufficient fees")
	}
	gatewaySettleDevshardOnChain = func(*Gateway, context.Context, string, adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		settleAttempts++
		return nil, nil
	}
	t.Cleanup(func() {
		gatewayCreateRotationEscrow = oldCreate
		gatewaySettleDevshardOnChain = oldSettle
	})

	g := &Gateway{store: store, rotationFailures: make(map[string]struct{})}
	g.finishBridgeEscrows(ChainPhaseSnapshot{EpochIndex: 11}, settings)
	g.finishBridgeEscrows(ChainPhaseSnapshot{EpochIndex: 11}, settings)

	require.Equal(t, 1, createAttempts)
	require.Equal(t, 0, settleAttempts)
}

func TestEscrowRotationFinishSettlesTempFromCurrentLatestEpoch(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		EscrowRotation: EscrowRotationSettings{
			Enabled:           true,
			SettlementEnabled: true,
			Models: []EscrowRotationModelSettings{{
				ModelID:       "Qwen/Test",
				TempCount:     1,
				TargetCount:   2,
				Amount:        1000,
				PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY",
			}},
		},
	}.WithTuningDefaults()
	require.NoError(t, store.Initialize(settings, []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "secret", Model: "Qwen/Test"},
		Active:        true,
		RotationRole:  rotationRoleTemp,
		RotationEpoch: 10,
	}}))

	oldCreate := gatewayCreateRotationEscrow
	oldSettle := gatewaySettleDevshardOnChain
	createAttempts := 0
	var settled []string
	gatewayCreateRotationEscrow = func(*Gateway, context.Context, GatewaySettings, EscrowRotationModelSettings, string, uint64) (*CreateDevshardEscrowResult, error) {
		createAttempts++
		return &CreateDevshardEscrowResult{EscrowID: uint64(90 + createAttempts), TxHash: "OK"}, nil
	}
	gatewaySettleDevshardOnChain = func(_ *Gateway, _ context.Context, id string, _ adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		settled = append(settled, id)
		return nil, nil
	}
	t.Cleanup(func() {
		gatewayCreateRotationEscrow = oldCreate
		gatewaySettleDevshardOnChain = oldSettle
	})

	g := &Gateway{store: store, rotationFailures: make(map[string]struct{})}
	g.finishBridgeEscrows(ChainPhaseSnapshot{EpochIndex: 10}, settings)

	require.Equal(t, 2, createAttempts)
	require.Equal(t, []string{"12"}, settled)
	statuses, err := store.LoadRotationStatuses(1)
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	require.Equal(t, "finish_regular", statuses[0].Stage)
	require.EqualValues(t, 2, statuses[0].CreatedCount)
	require.EqualValues(t, 1, statuses[0].SettledCount)
	require.True(t, statuses[0].Completed)
}

func TestEscrowRotationPrepareDeactivatesRegularWithoutSettlementWhenSettlementDisabled(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		EscrowRotation: EscrowRotationSettings{
			Enabled:           true,
			SettlementEnabled: false,
			Models: []EscrowRotationModelSettings{{
				ModelID:       "Qwen/Test",
				TempCount:     1,
				TargetCount:   2,
				Amount:        1000,
				PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY",
			}},
		},
	}.WithTuningDefaults()
	require.NoError(t, store.Initialize(settings, []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "secret", Model: "Qwen/Test"},
		Active:        true,
		RotationRole:  rotationRoleRegular,
		RotationEpoch: 9,
	}}))

	oldCreate := gatewayCreateRotationEscrow
	oldSettle := gatewaySettleDevshardOnChain
	createAttempts := 0
	settleAttempts := 0
	gatewayCreateRotationEscrow = func(*Gateway, context.Context, GatewaySettings, EscrowRotationModelSettings, string, uint64) (*CreateDevshardEscrowResult, error) {
		createAttempts++
		return &CreateDevshardEscrowResult{EscrowID: 99, TxHash: "OK"}, nil
	}
	gatewaySettleDevshardOnChain = func(*Gateway, context.Context, string, adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		settleAttempts++
		return nil, nil
	}
	t.Cleanup(func() {
		gatewayCreateRotationEscrow = oldCreate
		gatewaySettleDevshardOnChain = oldSettle
	})

	rt := &devshardRuntime{id: "12"}
	rt.active.Store(true)
	g := &Gateway{store: store, runtimes: map[string]*devshardRuntime{"12": rt}, rotationFailures: make(map[string]struct{})}
	g.prepareBridgeEscrows(ChainPhaseSnapshot{EpochIndex: 10}, settings)

	require.Equal(t, 1, createAttempts)
	require.Equal(t, 0, settleAttempts)
	require.False(t, rt.active.Load())
	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, gatewayDevshardsByID(state.Devshards)["12"].Active)
	statuses, err := store.LoadRotationStatuses(1)
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	require.EqualValues(t, 0, statuses[0].SettledCount)
	require.True(t, statuses[0].Completed)
}

func TestEscrowRotationFinishDeactivatesTempWithoutSettlementWhenSettlementDisabled(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		EscrowRotation: EscrowRotationSettings{
			Enabled:           true,
			SettlementEnabled: false,
			Models: []EscrowRotationModelSettings{{
				ModelID:       "Qwen/Test",
				TempCount:     1,
				TargetCount:   1,
				Amount:        1000,
				PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY",
			}},
		},
	}.WithTuningDefaults()
	require.NoError(t, store.Initialize(settings, []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "secret", Model: "Qwen/Test"},
		Active:        true,
		RotationRole:  rotationRoleTemp,
		RotationEpoch: 10,
	}}))

	oldCreate := gatewayCreateRotationEscrow
	oldSettle := gatewaySettleDevshardOnChain
	createAttempts := 0
	settleAttempts := 0
	gatewayCreateRotationEscrow = func(*Gateway, context.Context, GatewaySettings, EscrowRotationModelSettings, string, uint64) (*CreateDevshardEscrowResult, error) {
		createAttempts++
		return &CreateDevshardEscrowResult{EscrowID: 99, TxHash: "OK"}, nil
	}
	gatewaySettleDevshardOnChain = func(*Gateway, context.Context, string, adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		settleAttempts++
		return nil, nil
	}
	t.Cleanup(func() {
		gatewayCreateRotationEscrow = oldCreate
		gatewaySettleDevshardOnChain = oldSettle
	})

	rt := &devshardRuntime{id: "12"}
	rt.active.Store(true)
	g := &Gateway{store: store, runtimes: map[string]*devshardRuntime{"12": rt}, rotationFailures: make(map[string]struct{})}
	g.finishBridgeEscrows(ChainPhaseSnapshot{EpochIndex: 10}, settings)

	require.Equal(t, 1, createAttempts)
	require.Equal(t, 0, settleAttempts)
	require.False(t, rt.active.Load())
	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, gatewayDevshardsByID(state.Devshards)["12"].Active)
	statuses, err := store.LoadRotationStatuses(1)
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	require.EqualValues(t, 0, statuses[0].SettledCount)
	require.True(t, statuses[0].Completed)
}

func TestEscrowRotationPrepareRotatesModelsIndependently(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		EscrowRotation: EscrowRotationSettings{
			Enabled:           true,
			SettlementEnabled: true,
			Models: []EscrowRotationModelSettings{{
				ModelID:       "Qwen/Test",
				TempCount:     1,
				TargetCount:   2,
				Amount:        1000,
				PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY",
			}, {
				ModelID:       "Kimi/Test",
				TempCount:     1,
				TargetCount:   2,
				Amount:        1000,
				PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY",
			}},
		},
	}.WithTuningDefaults()
	require.NoError(t, store.Initialize(settings, []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "secret", Model: "Qwen/Test"},
		Active:        true,
		RotationRole:  rotationRoleRegular,
		RotationEpoch: 9,
	}, {
		RuntimeConfig: RuntimeConfig{ID: "13", PrivateKeyHex: "secret", Model: "Kimi/Test"},
		Active:        true,
		RotationRole:  rotationRoleRegular,
		RotationEpoch: 9,
	}}))

	oldCreate := gatewayCreateRotationEscrow
	oldSettle := gatewaySettleDevshardOnChain
	var settled []string
	gatewayCreateRotationEscrow = func(_ *Gateway, _ context.Context, _ GatewaySettings, model EscrowRotationModelSettings, _ string, _ uint64) (*CreateDevshardEscrowResult, error) {
		if model.ModelID == "Qwen/Test" {
			return nil, fmt.Errorf("epoch already has 100 escrows")
		}
		return &CreateDevshardEscrowResult{EscrowID: 99, TxHash: "OK"}, nil
	}
	gatewaySettleDevshardOnChain = func(_ *Gateway, _ context.Context, id string, _ adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		settled = append(settled, id)
		return nil, nil
	}
	t.Cleanup(func() {
		gatewayCreateRotationEscrow = oldCreate
		gatewaySettleDevshardOnChain = oldSettle
	})

	g := &Gateway{store: store, rotationFailures: make(map[string]struct{})}
	g.prepareBridgeEscrows(ChainPhaseSnapshot{EpochIndex: 10}, settings)

	require.Equal(t, []string{"13"}, settled)
	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	byID := gatewayDevshardsByID(state.Devshards)
	require.Equal(t, rotationRoleTemp, byID["12"].RotationRole)
	require.Equal(t, rotationRoleRegular, byID["13"].RotationRole)
}

func TestEscrowRotationUsesEpochSwitchHeightDuringPoC(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		EscrowRotation: EscrowRotationSettings{
			Enabled:           true,
			SettlementEnabled: true,
			PrePoCBlocks:      300,
			Models: []EscrowRotationModelSettings{{
				ModelID:       "Qwen/Test",
				TempCount:     1,
				TargetCount:   2,
				Amount:        1000,
				PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY",
			}},
		},
	}.WithTuningDefaults()
	require.NoError(t, store.Initialize(settings, []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "secret", Model: "Qwen/Test"},
		Active:        true,
		RotationRole:  rotationRoleRegular,
		RotationEpoch: 9,
	}}))

	oldCreate := gatewayCreateRotationEscrow
	oldSettle := gatewaySettleDevshardOnChain
	createAttempts := 0
	settleAttempts := 0
	gatewayCreateRotationEscrow = func(_ *Gateway, _ context.Context, _ GatewaySettings, _ EscrowRotationModelSettings, _ string, _ uint64) (*CreateDevshardEscrowResult, error) {
		createAttempts++
		return &CreateDevshardEscrowResult{EscrowID: 99, TxHash: "OK"}, nil
	}
	gatewaySettleDevshardOnChain = func(_ *Gateway, _ context.Context, _ string, _ adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		settleAttempts++
		return nil, nil
	}
	t.Cleanup(func() {
		gatewayCreateRotationEscrow = oldCreate
		gatewaySettleDevshardOnChain = oldSettle
	})

	g := &Gateway{
		store:            store,
		settings:         settings,
		phaseGate:        &ChainPhaseGate{},
		rotationFailures: make(map[string]struct{}),
	}
	g.phaseGate.storeSnapshot(ChainPhaseSnapshot{
		BlockHeight:            350,
		EpochIndex:             10,
		EpochPhase:             epochPhasePoCValidate,
		pocStartBlockHeight:    100,
		epochSwitchBlockHeight: 600,
	})

	g.rotateEscrowsOnce()

	require.Equal(t, 1, createAttempts)
	require.Equal(t, 1, settleAttempts)
}

func gatewayDevshardsByID(devshards []GatewayDevshardState) map[string]GatewayDevshardState {
	byID := make(map[string]GatewayDevshardState, len(devshards))
	for _, devshard := range devshards {
		byID[devshard.ID] = devshard
	}
	return byID
}
