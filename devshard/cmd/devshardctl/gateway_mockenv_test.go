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
