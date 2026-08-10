package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// Steps:
// - Create two active mock runtimes with different model IDs.
// - Send pooled chat for the first model through the real gateway handler.
// - Assert the gateway selects only the matching runtime.
func TestGatewayMockEnvPooledChatRoutesByModel(t *testing.T) {
	alpha := &gatewayMockRuntime{
		id:     "11",
		model:  "Qwen/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/v1/chat/completions", r.URL.Path)
			require.Contains(t, readRequestBodyForTest(t, r), `"model":"Qwen/Test"`)
			writeMockenvChatJSON(w, "11", "Qwen/Test")
		},
	}
	beta := &gatewayMockRuntime{
		id:     "22",
		model:  "Kimi/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			writeMockenvChatJSON(w, "22", "Kimi/Test")
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{alpha, beta}, withMockenvSettings(func(settings *GatewaySettings) {
		settings.ModelLimits = append(settings.ModelLimits, GatewayModelLimitSettings{
			ModelID:    "Kimi/Test",
			AccessMode: string(gatewayAccessModeOpen),
		})
	}))

	rec := env.postChat(mockenvChatBody("Qwen/Test", "hello"))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "11", rec.Header().Get("X-Devshard-ID"))
	require.Contains(t, rec.Body.String(), "from 11")
	require.EqualValues(t, 1, alpha.calls.Load())
	require.EqualValues(t, 0, beta.calls.Load())
}

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
// - Send pooled chat without a key and then with a user API key.
// - Assert only the authorized request reaches the runtime.
func TestGatewayMockEnvAPIKeyModelAccess(t *testing.T) {
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

	denied := env.postChat(mockenvChatBody("Qwen/Test", "private"))
	require.Equal(t, http.StatusUnauthorized, denied.Code)
	require.Contains(t, denied.Body.String(), "requires an API key")
	require.EqualValues(t, 0, rt.calls.Load())

	allowed := env.postChat(mockenvChatBody("Qwen/Test", "private"), withBearer(mockenvUserKey))
	require.Equal(t, http.StatusOK, allowed.Code)
	require.Equal(t, "12", allowed.Header().Get("X-Devshard-ID"))
	require.EqualValues(t, 1, rt.calls.Load())
}

// Steps:
// - Configure the model to require an admin API key.
// - Send pooled chat with a user key and then with the admin key.
// - Assert only the admin-authenticated request reaches the runtime.
func TestGatewayMockEnvAdminOnlyModelAccess(t *testing.T) {
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

	denied := env.postChat(mockenvChatBody("Qwen/Test", "admin only"), withBearer(mockenvUserKey))
	require.Equal(t, http.StatusUnauthorized, denied.Code)
	require.Contains(t, denied.Body.String(), "requires an admin API key")
	require.EqualValues(t, 0, rt.calls.Load())

	allowed := env.postChat(mockenvChatBody("Qwen/Test", "admin only"), withBearer(mockenvAdminKey))
	require.Equal(t, http.StatusOK, allowed.Code)
	require.Equal(t, "12", allowed.Header().Get("X-Devshard-ID"))
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
// - Send a pooled chat request that stores a cacheable runtime response.
// - Send the identical pooled chat request again.
// - Assert the second response is replayed from cache without another runtime call.
func TestGatewayMockEnvPooledChatCacheHitSkipsRuntime(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
	}
	rt.handler = func(w http.ResponseWriter, r *http.Request) {
		require.EqualValues(t, 1, rt.calls.Load(), "cache hit should skip repeated pooled runtime call")
		writeMockenvChatJSON(w, "12", "Qwen/Test")
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})
	body := mockenvChatBody("Qwen/Test", "cache me")

	first := env.postChat(body)
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "12", first.Header().Get("X-Devshard-ID"))
	require.Contains(t, first.Body.String(), "from 12")
	require.EqualValues(t, 1, rt.calls.Load())

	second := env.postChat(body)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, "12", second.Header().Get("X-Devshard-ID"))
	require.Equal(t, first.Body.String(), second.Body.String())
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
// - Configure only inactive runtimes for a supported pooled model.
// - Send pooled chat for that model.
// - Assert runtime selection fails before any runtime is called.
func TestGatewayMockEnvAllRuntimesUnavailableReturnsSelectionError(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: false,
		handler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("pooled chat should not reach unavailable runtimes")
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})

	rec := env.postChat(mockenvChatBody("Qwen/Test", "no available runtime"))

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "no devshard runtimes available for new inferences")
	require.Contains(t, rec.Body.String(), "inactive=1")
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
// - Create one inactive runtime and one active runtime for the same model.
// - Send pooled chat for that model.
// - Assert only the active runtime receives the request.
func TestGatewayMockEnvInactiveRuntimeExcludedFromPooledChat(t *testing.T) {
	inactive := &gatewayMockRuntime{
		id:     "cold",
		model:  "Qwen/Test",
		active: false,
		handler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("inactive runtime should not receive pooled chat")
		},
	}
	active := &gatewayMockRuntime{
		id:     "hot",
		model:  "Qwen/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			writeMockenvChatJSON(w, "hot", "Qwen/Test")
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{inactive, active})

	rec := env.postChat(mockenvChatBody("Qwen/Test", "skip inactive"))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "hot", rec.Header().Get("X-Devshard-ID"))
	require.EqualValues(t, 0, inactive.calls.Load())
	require.EqualValues(t, 1, active.calls.Load())
}

// Steps:
// - Create an active runtime for a supported model.
// - Send pooled chat for an unsupported model.
// - Assert the gateway rejects before calling any runtime.
func TestGatewayMockEnvUnsupportedModelRejectedBeforeRuntime(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:      "12",
		model:   "Qwen/Test",
		active:  true,
		handler: func(w http.ResponseWriter, r *http.Request) { t.Fatal("unsupported model should not reach runtime") },
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})

	rec := env.postChat(mockenvChatBody("Nope/Unsupported", "hello"))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "unsupported model")
	require.EqualValues(t, 0, rt.calls.Load())
}

// Steps:
// - Create an active runtime for the gateway default model.
// - Send pooled chat without a model field.
// - Assert the gateway routes by default model without injecting one into the body.
func TestGatewayMockEnvPooledChatUsesDefaultModel(t *testing.T) {
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

	rec := env.postChat(`{"messages":[{"role":"user","content":"use default"}]}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "12", rec.Header().Get("X-Devshard-ID"))
	require.EqualValues(t, 1, rt.calls.Load())
}

// Steps:
// - Create an active runtime for a supported model.
// - Send malformed JSON to pooled chat.
// - Assert the gateway rejects before calling the runtime.
func TestGatewayMockEnvMalformedJSONRejectedBeforeRuntime(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:      "12",
		model:   "Qwen/Test",
		active:  true,
		handler: func(w http.ResponseWriter, r *http.Request) { t.Fatal("malformed JSON should not reach runtime") },
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})

	rec := env.postChat(`{`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "parse request")
	require.EqualValues(t, 0, rt.calls.Load())
}

// Steps:
// - Create an active runtime that emits OpenAI-style SSE chunks.
// - Send pooled streaming chat through the gateway.
// - Assert SSE headers, chunks, and [DONE] pass through.
func TestGatewayMockEnvStreamingChatPassthrough(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			require.Contains(t, readRequestBodyForTest(t, r), `"stream":true`)
			writeMockenvChatSSE(w, "12", "Qwen/Test")
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt})

	body := `{"model":"Qwen/Test","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	rec := env.postChat(body)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "12", rec.Header().Get("X-Devshard-ID"))
	require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
	require.Contains(t, rec.Body.String(), "data:")
	require.Contains(t, rec.Body.String(), "data: [DONE]")
	require.EqualValues(t, 1, rt.calls.Load())
}

// Steps:
// - Pre-fill the gateway limiter for the target model.
// - Send another pooled chat request for that model.
// - Assert the gateway returns 429 before calling the runtime.
func TestGatewayMockEnvConcurrencyLimitRejectsBeforeRuntime(t *testing.T) {
	ConfigureCapacityAwareLimits("true")
	t.Cleanup(func() { ConfigureCapacityAwareLimits("") })

	limiter := NewGatewayLimiter(1, 0)
	require.NoError(t, limiter.AcquireForModelWithCapacity("Qwen/Test", 1, LimiterModelCapacity{ScaleFactor: 1}))
	t.Cleanup(func() { limiter.ReleaseForModel("Qwen/Test", 1) })

	rt := &gatewayMockRuntime{
		id:      "12",
		model:   "Qwen/Test",
		active:  true,
		handler: func(w http.ResponseWriter, r *http.Request) { t.Fatal("limited request should not reach runtime") },
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt}, withMockenvLimiter(limiter))

	rec := env.postChat(mockenvChatBody("Qwen/Test", "limited"))

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Contains(t, rec.Body.String(), "rate limit exceeded")
	require.EqualValues(t, 0, rt.calls.Load())
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
// - Create two active runtimes.
// - Request pooled gateway status.
// - Assert the gateway returns aggregate status instead of proxying a runtime.
func TestGatewayMockEnvMultiRuntimeStatusIsAggregate(t *testing.T) {
	alpha := &gatewayMockRuntime{
		id:     "11",
		model:  "Qwen/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("multi-runtime pooled status should not proxy runtime handlers")
		},
	}
	beta := &gatewayMockRuntime{
		id:     "22",
		model:  "Kimi/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("multi-runtime pooled status should not proxy runtime handlers")
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{alpha, beta}, withMockenvSettings(func(settings *GatewaySettings) {
		settings.ModelLimits = append(settings.ModelLimits, GatewayModelLimitSettings{
			ModelID:    "Kimi/Test",
			AccessMode: string(gatewayAccessModeOpen),
		})
	}))

	rec := env.get("/v1/status")

	require.Equal(t, http.StatusOK, rec.Code)
	requireMockenvJSONField(t, rec.Body, "mode", "gateway")
	requireMockenvJSONField(t, rec.Body, "runtimes", float64(2))
	require.EqualValues(t, 0, alpha.calls.Load())
	require.EqualValues(t, 0, beta.calls.Load())
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
