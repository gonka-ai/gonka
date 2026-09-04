package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
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
			assert.Equal(t, "/v1/chat/completions", r.URL.Path)
			assert.Contains(t, readRequestBodyForTest(t, r), `"model":"Qwen/Test"`)
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
		assert.EqualValues(t, 1, rt.calls.Load(), "cache hit should skip repeated pooled runtime call")
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
// - Configure an `api_key` model and store a pooled chat response with a valid key.
// - Send the identical pooled chat request without credentials.
// - Assert the cache lookup does not bypass model access checks.
func TestGatewayMockEnvPooledChatCacheDoesNotBypassAccessMode(t *testing.T) {
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
	body := mockenvChatBody("Qwen/Test", "cache protected")

	first := env.postChat(body, withBearer(mockenvUserKey))
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "12", first.Header().Get("X-Devshard-ID"))
	require.EqualValues(t, 1, rt.calls.Load())

	unauthorized := env.postChat(body)
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code)
	require.Contains(t, unauthorized.Body.String(), "requires an API key")
	require.EqualValues(t, 1, rt.calls.Load())

	cached := env.postChat(body, withBearer(mockenvUserKey))
	require.Equal(t, http.StatusOK, cached.Code)
	require.Equal(t, "12", cached.Header().Get("X-Devshard-ID"))
	require.Equal(t, first.Body.String(), cached.Body.String())
	require.EqualValues(t, 1, rt.calls.Load())
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
			assert.NotContains(t, body, `"model"`)
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
// - Configure the gateway default model to require a user API key.
// - Send pooled chat without a `model` field and without credentials.
// - Assert effective default-model access is enforced before runtime forwarding.
func TestGatewayMockEnvPooledChatMissingModelEnforcesDefaultModelAccess(t *testing.T) {
	rt := &gatewayMockRuntime{
		id:     "12",
		model:  "Qwen/Test",
		active: true,
		handler: func(w http.ResponseWriter, r *http.Request) {
			assert.NotContains(t, readRequestBodyForTest(t, r), `"model"`)
			writeMockenvChatJSON(w, "12", "Qwen/Test")
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{rt}, withMockenvSettings(func(settings *GatewaySettings) {
		settings.ModelLimits = []GatewayModelLimitSettings{{
			ModelID:    "Qwen/Test",
			AccessMode: string(gatewayAccessModeAPIKey),
		}}
	}))
	body := `{"messages":[{"role":"user","content":"default model still private"}]}`

	denied := env.postChat(body)
	require.Equal(t, http.StatusUnauthorized, denied.Code)
	require.Contains(t, denied.Body.String(), "requires an API key")
	require.EqualValues(t, 0, rt.calls.Load())

	allowed := env.postChat(body, withBearer(mockenvUserKey))
	require.Equal(t, http.StatusOK, allowed.Code)
	require.Equal(t, "12", allowed.Header().Get("X-Devshard-ID"))
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
			assert.Contains(t, readRequestBodyForTest(t, r), `"stream":true`)
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
// - Configure two pooled runtimes whose participant host is probe-quarantined.
// - Send pooled chat for their model through the full gateway handler.
// - Assert participant capacity rejection returns 503 before any runtime call.
func TestGatewayMockEnvPooledChatParticipantLimiterAllHostsRejectedBeforeRuntime(t *testing.T) {
	limiter := NewParticipantRequestLimiter(1, 10)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	alpha := &gatewayMockRuntime{
		id:                    "12",
		model:                 "Qwen/Test",
		active:                true,
		participantKeys:       []string{"shared-host"},
		participantSlotCounts: map[string]int{"shared-host": 1},
		handler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("participant-limited pooled request should not reach first runtime")
		},
	}
	beta := &gatewayMockRuntime{
		id:                    "22",
		model:                 "Qwen/Test",
		active:                true,
		participantKeys:       []string{"shared-host"},
		participantSlotCounts: map[string]int{"shared-host": 1},
		handler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("participant-limited pooled request should not reach second runtime")
		},
	}
	env := newGatewayMockEnv(t, []*gatewayMockRuntime{alpha, beta})
	env.gateway.participantLimiter = limiter
	limiter.SetMetrics(env.gateway.metrics)
	env.gateway.attachCapacityLiveAvailability()

	rec := env.postChat(mockenvChatBody("Qwen/Test", "all hosts quarantined"))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "no live host capacity")
	require.EqualValues(t, 0, alpha.calls.Load())
	require.EqualValues(t, 0, beta.calls.Load())
}
