package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// Steps:
// - Create one active mock runtime.
// - Send chat through the /devshard/{id} route.
// - Assert the gateway rewrites the inner path and forwards to that runtime.
func TestGatewayMockEnvDirectDevshardRouteByID(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/v1/chat/completions", r.URL.Path)
			require.Equal(t, "/v1/chat/completions", r.RequestURI)
			writeMockenvChatJSON(w, "12", "Qwen/Test")
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})

	rec := env.postDirectChat("12", mockenvChatBody("Qwen/Test", "direct"))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "12", rec.Header().Get("X-Devshard-ID"))
	require.Contains(t, rec.Body.String(), "from 12")
	require.EqualValues(t, 1, rt.calls.Load())
}

// Steps:
// - Configure the model to require a gateway API key.
// - Send direct devshard chat without a key and then with a user API key.
// - Assert the direct route does not bypass model access checks.
func TestGatewayMockEnvDirectDevshardEnforcesAPIKeyModelAccess(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt}, withMockenvSettings(func(settings *GatewaySettings) {
		settings.ModelLimits = []GatewayModelLimitSettings{{
			ModelID:    "Qwen/Test",
			AccessMode: string(gatewayAccessModeAPIKey),
		}}
	}))

	denied := env.postDirectChat("12", mockenvChatBody("Qwen/Test", "direct private"))
	require.Equal(t, http.StatusUnauthorized, denied.Code)
	require.Contains(t, denied.Body.String(), "requires an API key")
	require.EqualValues(t, 0, rt.calls.Load())

	allowed := env.postDirectChat("12", mockenvChatBody("Qwen/Test", "direct private"), withBearer(mockenvUserKey))
	require.Equal(t, http.StatusOK, allowed.Code)
	require.Equal(t, "12", allowed.Header().Get("X-Devshard-ID"))
	require.EqualValues(t, 1, rt.calls.Load())
}

// Steps:
// - Configure the model to require an admin API key.
// - Send direct devshard chat with a user key and then with the admin key.
// - Assert only the admin-authenticated direct request reaches the runtime.
func TestGatewayMockEnvDirectDevshardEnforcesAdminOnlyModelAccess(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt}, withMockenvSettings(func(settings *GatewaySettings) {
		settings.ModelLimits = []GatewayModelLimitSettings{{
			ModelID:    "Qwen/Test",
			AccessMode: string(gatewayAccessModeAdminOnly),
		}}
	}))

	denied := env.postDirectChat("12", mockenvChatBody("Qwen/Test", "direct admin only"), withBearer(mockenvUserKey))
	require.Equal(t, http.StatusUnauthorized, denied.Code)
	require.Contains(t, denied.Body.String(), "requires an admin API key")
	require.EqualValues(t, 0, rt.calls.Load())

	allowed := env.postDirectChat("12", mockenvChatBody("Qwen/Test", "direct admin only"), withBearer(mockenvAdminKey))
	require.Equal(t, http.StatusOK, allowed.Code)
	require.Equal(t, "12", allowed.Header().Get("X-Devshard-ID"))
	require.EqualValues(t, 1, rt.calls.Load())
}

// Steps:
// - Send a direct devshard chat request that stores a cacheable runtime response.
// - Send the identical direct devshard chat request again.
// - Assert the direct-route cache branch replays the response without forwarding.
func TestGatewayMockEnvDirectDevshardCacheHitSkipsRuntime(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
	}
	rt.handler = func(w http.ResponseWriter, r *http.Request) {
		require.EqualValues(t, 1, rt.calls.Load(), "cache hit should skip repeated direct runtime call")
		writeMockenvChatJSON(w, "12", "Qwen/Test")
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})
	body := mockenvChatBody("Qwen/Test", "direct cache me")

	first := env.postDirectChat("12", body)
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "12", first.Header().Get("X-Devshard-ID"))
	require.Contains(t, first.Body.String(), "from 12")
	require.EqualValues(t, 1, rt.calls.Load())

	second := env.postDirectChat("12", body)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, "12", second.Header().Get("X-Devshard-ID"))
	require.Equal(t, first.Body.String(), second.Body.String())
	require.EqualValues(t, 1, rt.calls.Load())
}

// Steps:
// - Store a direct chat cache entry while the runtime is active.
// - Mark the resident runtime inactive before sending the identical request again.
// - Assert the direct cache path does not bypass runtime availability checks.
func TestGatewayMockEnvDirectDevshardCacheDoesNotBypassInactiveRuntime(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
	}
	rt.handler = func(w http.ResponseWriter, r *http.Request) {
		require.EqualValues(t, 1, rt.calls.Load(), "only the initial active request should reach runtime")
		writeMockenvChatJSON(w, "12", "Qwen/Test")
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})
	body := mockenvChatBody("Qwen/Test", "cached before inactive")

	first := env.postDirectChat("12", body)
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "12", first.Header().Get("X-Devshard-ID"))
	require.EqualValues(t, 1, rt.calls.Load())

	env.gateway.mu.Lock()
	resident := env.gateway.runtimes["12"]
	env.gateway.mu.Unlock()
	require.NotNil(t, resident)
	resident.active.Store(false)
	require.NoError(t, env.gateway.store.SetDevshardActive("12", false))

	cached := env.postDirectChat("12", body)
	require.Equal(t, http.StatusConflict, cached.Code)
	require.Contains(t, cached.Body.String(), "unavailable for new inferences")
	require.Contains(t, cached.Body.String(), "inactive")
	require.NotEqual(t, first.Body.String(), cached.Body.String())
	require.EqualValues(t, 1, rt.calls.Load())
}

// Steps:
// - Configure an `api_key` model and store a direct chat response with a valid key.
// - Send the identical direct chat request without credentials.
// - Assert the direct-route cache lookup does not bypass model access checks.
func TestGatewayMockEnvDirectDevshardCacheDoesNotBypassAccessMode(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt}, withMockenvSettings(func(settings *GatewaySettings) {
		settings.ModelLimits = []GatewayModelLimitSettings{{
			ModelID:    "Qwen/Test",
			AccessMode: string(gatewayAccessModeAPIKey),
		}}
	}))
	body := mockenvChatBody("Qwen/Test", "direct cache protected")

	first := env.postDirectChat("12", body, withBearer(mockenvUserKey))
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "12", first.Header().Get("X-Devshard-ID"))
	require.EqualValues(t, 1, rt.calls.Load())

	unauthorized := env.postDirectChat("12", body)
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code)
	require.Contains(t, unauthorized.Body.String(), "requires an API key")
	require.EqualValues(t, 1, rt.calls.Load())

	cached := env.postDirectChat("12", body, withBearer(mockenvUserKey))
	require.Equal(t, http.StatusOK, cached.Code)
	require.Equal(t, "12", cached.Header().Get("X-Devshard-ID"))
	require.Equal(t, first.Body.String(), cached.Body.String())
	require.EqualValues(t, 1, rt.calls.Load())
}

// Steps:
// - Send the same model-less direct chat body to two runtimes with different models.
// - Assert the second runtime is called instead of receiving the first model's cache entry.
// - Assert a repeated request to the second runtime then hits its own scoped cache entry.
func TestGatewayMockEnvDirectDevshardCacheIsScopedByModel(t *testing.T) {
	alpha := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
	}
	alpha.handler = func(w http.ResponseWriter, r *http.Request) {
		require.NotContains(t, readRequestBodyForTest(t, r), `"model"`)
		require.EqualValues(t, 1, alpha.calls.Load(), "Qwen cache should only be populated once")
		writeMockenvChatJSON(w, "12", "Qwen/Test")
	}
	beta := &gatewayMockRuntime{
		id:     "22",
		model:  "Kimi/Test",
		active: true,
	}
	beta.handler = func(w http.ResponseWriter, r *http.Request) {
		require.NotContains(t, readRequestBodyForTest(t, r), `"model"`)
		require.EqualValues(t, 1, beta.calls.Load(), "Kimi request should miss Qwen-scoped cache")
		writeMockenvChatJSON(w, "22", "Kimi/Test")
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{alpha, beta}, withMockenvSettings(func(settings *GatewaySettings) {
		settings.ModelLimits = append(settings.ModelLimits, GatewayModelLimitSettings{
			ModelID:    "Kimi/Test",
			AccessMode: string(gatewayAccessModeOpen),
		})
	}))
	body := `{"messages":[{"role":"user","content":"same model-less direct body"}]}`

	first := env.postDirectChat("12", body)
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "12", first.Header().Get("X-Devshard-ID"))
	require.Contains(t, first.Body.String(), "from 12")
	require.EqualValues(t, 1, alpha.calls.Load())
	require.EqualValues(t, 0, beta.calls.Load())

	second := env.postDirectChat("22", body)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, "22", second.Header().Get("X-Devshard-ID"))
	require.Contains(t, second.Body.String(), "from 22")
	require.EqualValues(t, 1, alpha.calls.Load())
	require.EqualValues(t, 1, beta.calls.Load())

	cached := env.postDirectChat("22", body)
	require.Equal(t, http.StatusOK, cached.Code)
	require.Equal(t, "22", cached.Header().Get("X-Devshard-ID"))
	require.Equal(t, second.Body.String(), cached.Body.String())
	require.EqualValues(t, 1, alpha.calls.Load())
	require.EqualValues(t, 1, beta.calls.Load())
}

// Steps:
// - Create an inactive but resident runtime for a supported model.
// - Send direct devshard chat to that runtime.
// - Assert the gateway returns conflict before forwarding to the runtime.
func TestGatewayMockEnvInactiveDirectDevshardReturnsConflict(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: false,
		handler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("inactive direct runtime should not receive chat")
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})

	rec := env.postDirectChat("12", mockenvChatBody("Qwen/Test", "inactive direct"))

	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "unavailable for new inferences")
	require.Contains(t, rec.Body.String(), "inactive")
	require.EqualValues(t, 0, rt.calls.Load())
}

// Steps:
// - Create an active runtime for one model.
// - Send direct devshard chat with a different requested model.
// - Assert the gateway rejects before forwarding to the runtime handler.
func TestGatewayMockEnvDirectDevshardRejectsWrongModel(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("wrong-model direct request should not reach runtime")
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt}, withMockenvSettings(func(settings *GatewaySettings) {
		settings.ModelLimits = append(settings.ModelLimits, GatewayModelLimitSettings{
			ModelID:    "Kimi/Test",
			AccessMode: string(gatewayAccessModeOpen),
		})
	}))

	rec := env.postDirectChat("12", mockenvChatBody("Kimi/Test", "wrong model"))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "unsupported model")
	require.EqualValues(t, 0, rt.calls.Load())
}

// Steps:
// - Create an active runtime for a supported model.
// - Send malformed JSON to direct devshard chat.
// - Assert the gateway rejects before calling the runtime.
func TestGatewayMockEnvDirectDevshardRejectsMalformedJSONBeforeRuntime(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("malformed direct JSON should not reach runtime")
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})

	rec := env.postDirectChat("12", `{`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "parse request")
	require.EqualValues(t, 0, rt.calls.Load())
}

// Steps:
// - Create an active runtime for its configured model.
// - Send direct devshard chat without a model field.
// - Assert the direct route uses the runtime model without injecting one into the body.
func TestGatewayMockEnvDirectDevshardUsesDefaultRuntimeModelWhenModelMissing(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			body := readRequestBodyForTest(t, r)
			require.NotContains(t, body, `"model"`)
			writeMockenvChatJSON(w, "12", "Qwen/Test")
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})

	rec := env.postDirectChat("12", `{"messages":[{"role":"user","content":"use runtime default"}]}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "12", rec.Header().Get("X-Devshard-ID"))
	require.Contains(t, rec.Body.String(), "from 12")
	require.EqualValues(t, 1, rt.calls.Load())
}

// Steps:
// - Configure the runtime model to require a user API key.
// - Send direct devshard chat without a `model` field and without credentials.
// - Assert effective runtime-model access is enforced before runtime forwarding.
func TestGatewayMockEnvDirectDevshardMissingModelEnforcesRuntimeModelAccess(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			require.NotContains(t, readRequestBodyForTest(t, r), `"model"`)
			writeMockenvChatJSON(w, "12", "Qwen/Test")
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt}, withMockenvSettings(func(settings *GatewaySettings) {
		settings.ModelLimits = []GatewayModelLimitSettings{{
			ModelID:    "Qwen/Test",
			AccessMode: string(gatewayAccessModeAPIKey),
		}}
	}))
	body := `{"messages":[{"role":"user","content":"runtime model still private"}]}`

	denied := env.postDirectChat("12", body)
	require.Equal(t, http.StatusUnauthorized, denied.Code)
	require.Contains(t, denied.Body.String(), "requires an API key")
	require.EqualValues(t, 0, rt.calls.Load())

	allowed := env.postDirectChat("12", body, withBearer(mockenvUserKey))
	require.Equal(t, http.StatusOK, allowed.Code)
	require.Equal(t, "12", allowed.Header().Get("X-Devshard-ID"))
	require.EqualValues(t, 1, rt.calls.Load())
}

// Steps:
// - Pre-fill the gateway limiter for the direct route target model.
// - Send direct devshard chat for that model.
// - Assert the gateway returns 429 before calling the runtime.
func TestGatewayMockEnvDirectDevshardLimiterRejectsBeforeRuntime(t *testing.T) {
	ConfigureCapacityAwareLimits("true")
	t.Cleanup(func() { ConfigureCapacityAwareLimits("") })

	limiter := NewGatewayLimiter(1, 0)
	require.NoError(t, limiter.AcquireForModelWithCapacity("Qwen/Test", 1, LimiterModelCapacity{ScaleFactor: 1}))
	t.Cleanup(func() { limiter.ReleaseForModel("Qwen/Test", 1) })

	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("limited direct request should not reach runtime")
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt}, withMockenvLimiter(limiter))

	rec := env.postDirectChat("12", mockenvChatBody("Qwen/Test", "direct limited"))

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Contains(t, rec.Body.String(), "rate limit exceeded")
	require.EqualValues(t, 0, rt.calls.Load())
}

// Steps:
// - Pre-fill the direct route limiter for a request body that is not yet cached.
// - Assert the limited request does not call the runtime or create a cache entry.
// - Release the limiter and assert the same body reaches the runtime once, then caches.
func TestGatewayMockEnvDirectDevshardLimiterRunsBeforeCacheMissForward(t *testing.T) {
	ConfigureCapacityAwareLimits("true")
	t.Cleanup(func() { ConfigureCapacityAwareLimits("") })

	limiter := NewGatewayLimiter(1, 0)
	require.NoError(t, limiter.AcquireForModelWithCapacity("Qwen/Test", 1, LimiterModelCapacity{ScaleFactor: 1}))

	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
	}
	rt.handler = func(w http.ResponseWriter, r *http.Request) {
		require.EqualValues(t, 1, rt.calls.Load(), "only the first allowed request should reach runtime")
		writeMockenvChatJSON(w, "12", "Qwen/Test")
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt}, withMockenvLimiter(limiter))
	body := mockenvChatBody("Qwen/Test", "limited before cache")

	limited := env.postDirectChat("12", body)
	require.Equal(t, http.StatusTooManyRequests, limited.Code)
	require.Contains(t, limited.Body.String(), "rate limit exceeded")
	require.EqualValues(t, 0, rt.calls.Load())

	limiter.ReleaseForModel("Qwen/Test", 1)

	allowed := env.postDirectChat("12", body)
	require.Equal(t, http.StatusOK, allowed.Code)
	require.Equal(t, "12", allowed.Header().Get("X-Devshard-ID"))
	require.EqualValues(t, 1, rt.calls.Load())

	cached := env.postDirectChat("12", body)
	require.Equal(t, http.StatusOK, cached.Code)
	require.Equal(t, "12", cached.Header().Get("X-Devshard-ID"))
	require.Equal(t, allowed.Body.String(), cached.Body.String())
	require.EqualValues(t, 1, rt.calls.Load())
}

// Steps:
// - Enable the gateway disabled response.
// - Send direct devshard chat through the real gateway handler.
// - Assert the disabled gateway response cannot be bypassed through the direct route.
func TestGatewayMockEnvDisabledGatewayBlocksDirectDevshardChat(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:      "12",
		model:   "Qwen/Test",
		active:  true,
		handler: func(w http.ResponseWriter, r *http.Request) { t.Fatal("disabled direct chat should not reach runtime") },
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt}, withMockenvSettings(func(settings *GatewaySettings) {
		settings.Disabled = GatewayDisabledSettings{
			Enabled: true,
			Message: "direct route is disabled too",
			NewURL:  "https://example.test/v1/chat/completions",
		}
	}))

	rec := env.postDirectChat("12", mockenvChatBody("Qwen/Test", "disabled direct"))

	require.Equal(t, http.StatusPermanentRedirect, rec.Code)
	require.Contains(t, rec.Body.String(), "direct route is disabled too")
	require.EqualValues(t, 0, rt.calls.Load())
}

// Steps:
// - Register an inactive devshard only in the gateway store, not in memory.
// - Request its public direct `/v1/status` path through the real handler stack.
// - Assert the gateway serves only cheap metadata and does not hydrate runtime state.
func TestGatewayMockEnvNonResidentDevshardServesPublicMetadataOnly(t *testing.T) {
	env := newGatewayMockEnv(t, nil)
	require.NoError(t, env.gateway.store.UpsertDevshard(GatewayDevshardState{
		RuntimeConfig: RuntimeConfig{
			ID:          "77",
			Model:       "Qwen/Test",
			StoragePath: t.TempDir(),
		},
		Active:            false,
		SettlementPending: true,
		RotationRole:      "candidate",
		RotationEpoch:     9,
	}))

	rec := env.get("/devshard/77/v1/status")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "77", rec.Header().Get("X-Devshard-ID"))
	require.Equal(t, "1", rec.Header().Get("X-Devshard-Readonly"))
	require.Equal(t, "1", rec.Header().Get("X-Devshard-Metadata-Only"))

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "77", body["id"])
	require.Equal(t, "Qwen/Test", body["model"])
	require.Equal(t, false, body["active"])
	require.Equal(t, false, body["resident"])
	require.Equal(t, true, body["metadata_only"])
	require.Equal(t, true, body["settlement_pending"])
	require.Equal(t, "candidate", body["rotation_role"])
	require.Equal(t, float64(9), body["rotation_epoch"])
	require.NotContains(t, body, "nonce")
	require.NotContains(t, body, "balance")
	require.NotContains(t, body, "phase")
}

// Steps:
// - Register an inactive devshard only in the gateway store, not in memory.
// - Request its direct `/v1/status` path with admin auth through the real handler stack.
// - Assert admin reads do not fall back to the public metadata-only response.
func TestGatewayMockEnvNonResidentDevshardAdminReadHydratesOrFailsWithoutMetadataFallback(t *testing.T) {
	env := newGatewayMockEnv(t, nil)
	require.NoError(t, env.gateway.store.UpsertDevshard(GatewayDevshardState{
		RuntimeConfig: RuntimeConfig{
			ID:          "77",
			Model:       "Qwen/Test",
			StoragePath: t.TempDir(),
		},
		Active: false,
	}))

	rec := env.get("/devshard/77/v1/status", withBearer(mockenvAdminKey))

	require.Empty(t, rec.Header().Get("X-Devshard-Metadata-Only"))
	require.NotContains(t, rec.Body.String(), `"metadata_only"`)
}

// Steps:
// - Create a gateway with one active runtime.
// - Send direct chat to an unknown devshard ID.
// - Assert the gateway returns 404 and does not call any runtime.
func TestGatewayMockEnvUnknownDirectDevshardReturnsNotFound(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})

	rec := env.postDirectChat("404", mockenvChatBody("Qwen/Test", "missing"))

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "unknown devshard 404")
	require.EqualValues(t, 0, rt.calls.Load())
}
