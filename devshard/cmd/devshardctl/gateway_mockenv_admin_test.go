package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// Steps:
// - Enable the gateway disabled response.
// - Send public chat and assert the replacement response is returned.
// - Send authenticated admin state and assert admin paths still work.
func TestGatewayMockEnvDisabledGatewayStillAllowsAdminState(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt}, withMockenvSettings(func(settings *GatewaySettings) {
		settings.Disabled = GatewayDisabledSettings{
			Enabled: true,
			Message: "use the replacement endpoint",
			NewURL:  "https://example.test/v1/chat/completions",
		}
	}))

	chat := env.postChat(mockenvChatBody("Qwen/Test", "disabled"))
	require.Equal(t, http.StatusPermanentRedirect, chat.Code)
	require.Contains(t, chat.Body.String(), "use the replacement endpoint")

	admin := env.get("/v1/admin/state", withBearer(mockenvAdminKey))
	require.Equal(t, http.StatusOK, admin.Code)
	require.Contains(t, admin.Body.String(), `"settings"`)
}

// Steps:
// - Enable the gateway disabled response.
// - Assert direct chat is still blocked, even with the admin key.
// - Assert an admin direct operational path is allowed only with the admin key.
func TestGatewayMockEnvDirectDevshardDisabledGatewayAllowsAdminOperationalPathOnly(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/v1/state", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"state":"ok"}`))
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt}, withMockenvSettings(func(settings *GatewaySettings) {
		settings.Disabled = GatewayDisabledSettings{
			Enabled: true,
			Message: "gateway paused",
			NewURL:  "https://example.test/v1/chat/completions",
		}
	}))

	chat := env.postDirectChat("12", mockenvChatBody("Qwen/Test", "admin cannot bypass disabled"), withBearer(mockenvAdminKey))
	require.Equal(t, http.StatusPermanentRedirect, chat.Code)
	require.Contains(t, chat.Body.String(), "gateway paused")
	require.EqualValues(t, 0, rt.calls.Load())

	missing := env.do(http.MethodGet, "/devshard/12/v1/state", "")
	require.Equal(t, http.StatusUnauthorized, missing.Code)
	require.Contains(t, missing.Body.String(), "Invalid admin API key")
	require.EqualValues(t, 0, rt.calls.Load())

	state := env.do(http.MethodGet, "/devshard/12/v1/state", "", withBearer(mockenvAdminKey))
	require.Equal(t, http.StatusOK, state.Code)
	require.Equal(t, "12", state.Header().Get("X-Devshard-ID"))
	require.Contains(t, state.Body.String(), `"state":"ok"`)
	require.EqualValues(t, 1, rt.calls.Load())
}

// Steps:
// - Create a store-backed gateway mock environment.
// - Request admin state with no key, a wrong key, and the admin key.
// - Assert only the valid admin key can read state.
func TestGatewayMockEnvAdminStateRequiresAdminKey(t *testing.T) {
	rt := &gatewayMockRuntime{id: "12", model: "Qwen/Test", active: true}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})

	missing := env.get("/v1/admin/state")
	require.Equal(t, http.StatusUnauthorized, missing.Code)
	require.Contains(t, missing.Body.String(), "Invalid admin API key")

	wrong := env.get("/v1/admin/state", withBearer("wrong-key"))
	require.Equal(t, http.StatusUnauthorized, wrong.Code)

	ok := env.get("/v1/admin/state", withBearer(mockenvAdminKey))
	require.Equal(t, http.StatusOK, ok.Code)
	require.Contains(t, ok.Body.String(), `"devshards"`)
}

// Steps:
// - Exercise direct operational paths that are admin-gated by middleware.
// - Send each path without credentials, with a wrong key, and with the admin key.
// - Assert only admin-authenticated requests reach the runtime handler.
func TestGatewayMockEnvAdminAuthRequiredForDirectOperationalPaths(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		path      string
		innerPath string
	}{
		{
			name:      "finalize",
			method:    http.MethodPost,
			path:      "/devshard/12/v1/finalize",
			innerPath: "/v1/finalize",
		},
		{
			name:      "state",
			method:    http.MethodGet,
			path:      "/devshard/12/v1/state",
			innerPath: "/v1/state",
		},
		{
			name:      "debug_state",
			method:    http.MethodGet,
			path:      "/devshard/12/v1/debug/state",
			innerPath: "/v1/debug/state",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := &gatewayMockRuntime{
				id:     "12",
				model:  "Qwen/Test",
				active: true,
				handler: func(w http.ResponseWriter, r *http.Request) {
					require.Equal(t, tc.innerPath, r.URL.Path)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"ok":true}`))
				},
			}
			env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})

			missing := env.do(tc.method, tc.path, "")
			require.Equal(t, http.StatusUnauthorized, missing.Code)
			require.Contains(t, missing.Body.String(), "Invalid admin API key")
			require.EqualValues(t, 0, rt.calls.Load())

			wrong := env.do(tc.method, tc.path, "", withBearer("wrong-key"))
			require.Equal(t, http.StatusUnauthorized, wrong.Code)
			require.EqualValues(t, 0, rt.calls.Load())

			ok := env.do(tc.method, tc.path, "", withBearer(mockenvAdminKey))
			require.Equal(t, http.StatusOK, ok.Code)
			require.Equal(t, "12", ok.Header().Get("X-Devshard-ID"))
			require.Contains(t, ok.Body.String(), `"ok":true`)
			require.EqualValues(t, 1, rt.calls.Load())
		})
	}
}

// Steps:
// - Send authenticated direct finalize to an active runtime.
// - Assert the gateway rewrites the request to /v1/finalize and forwards it.
// - Assert a successful finalize marks both in-memory runtime and stored state inactive.
func TestGatewayMockEnvDirectFinalizeMarksRuntimeInactive(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodPost, r.Method)
			require.Equal(t, "/v1/finalize", r.URL.Path)
			require.Equal(t, "/v1/finalize", r.RequestURI)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"finalized":true}`))
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})

	finalize := env.do(http.MethodPost, "/devshard/12/v1/finalize", "", withBearer(mockenvAdminKey))
	require.Equal(t, http.StatusOK, finalize.Code)
	require.Equal(t, "12", finalize.Header().Get("X-Devshard-ID"))
	require.Contains(t, finalize.Body.String(), `"finalized":true`)
	require.EqualValues(t, 1, rt.calls.Load())

	env.gateway.mu.Lock()
	resident := env.gateway.runtimes["12"]
	env.gateway.mu.Unlock()
	require.NotNil(t, resident)
	require.False(t, resident.active.Load())

	record, ok, err := env.gateway.store.GetDevshard("12")
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, record.Active)

	chat := env.postDirectChat("12", mockenvChatBody("Qwen/Test", "after finalize"))
	require.Equal(t, http.StatusConflict, chat.Code)
	require.Contains(t, chat.Body.String(), "unavailable for new inferences")
	require.EqualValues(t, 1, rt.calls.Load())
}

// Steps:
// - Store a private key in the gateway registry for one active runtime.
// - Request admin state through the real authenticated gateway handler.
// - Assert the read API does not expose the private key material.
func TestGatewayMockEnvAdminStateDoesNotExposePrivateKey(t *testing.T) {
	const privateKey = "super-secret-private-key"
	rt := &gatewayMockRuntime{
		id:            "12",
		model:         "Qwen/Test",
		active:        true,
		privateKeyHex: privateKey,
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})

	rec := env.get("/v1/admin/state", withBearer(mockenvAdminKey))

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), privateKey)
	require.NotContains(t, rec.Body.String(), `"private_key"`)
}
