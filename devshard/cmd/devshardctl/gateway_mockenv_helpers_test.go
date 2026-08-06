package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	mockenvDefaultModel = "Qwen/Test"
	mockenvAdminKey     = "admin-key"
	mockenvUserKey      = "user-key"
)

type gatewayMockEnv struct {
	t       *testing.T
	gateway *Gateway
	handler http.Handler
}

type gatewayMockRuntime struct {
	id            string
	model         string
	active        bool
	privateKeyHex string
	privateKeyEnv string
	handler       http.HandlerFunc
	calls         atomic.Int64
}

type gatewayMockOption func(*gatewayMockConfig)

type gatewayMockConfig struct {
	limiter  *GatewayLimiter
	settings GatewaySettings
	adminKey string
	apiKeys  map[string]struct{}
}

func newGatewayMockEnv(t *testing.T, runtimes []*gatewayMockRuntime, opts ...gatewayMockOption) *gatewayMockEnv {
	t.Helper()

	cfg := gatewayMockConfig{
		limiter:  NewGatewayLimiter(0, 0),
		adminKey: mockenvAdminKey,
		apiKeys:  map[string]struct{}{mockenvUserKey: {}},
		settings: GatewaySettings{
			DefaultModel: mockenvDefaultModel,
			ModelLimits: []GatewayModelLimitSettings{{
				ModelID:    mockenvDefaultModel,
				AccessMode: string(gatewayAccessModeOpen),
			}},
		}.WithTuningDefaults(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	devshards := make([]*devshardRuntime, 0, len(runtimes))
	for _, rt := range runtimes {
		devshards = append(devshards, rt.runtime(t))
	}

	g := NewGateway(devshards, cfg.limiter, cfg.settings.DefaultModel)
	g.settings = cfg.settings
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.Initialize(cfg.settings, gatewayMockStates(runtimes, devshards)))
	g.store = store
	handler := buildGatewayHandler(g, runtimeOptions{
		adminAPIKey: cfg.adminKey,
		apiKeys:     cfg.apiKeys,
	})

	return &gatewayMockEnv{t: t, gateway: g, handler: handler}
}

func gatewayMockStates(mocks []*gatewayMockRuntime, runtimes []*devshardRuntime) []GatewayDevshardState {
	states := make([]GatewayDevshardState, 0, len(runtimes))
	for i, rt := range runtimes {
		if rt == nil {
			continue
		}
		var privateKeyHex, privateKeyEnv string
		if i < len(mocks) && mocks[i] != nil {
			privateKeyHex = mocks[i].privateKeyHex
			privateKeyEnv = mocks[i].privateKeyEnv
		}
		states = append(states, GatewayDevshardState{
			RuntimeConfig: RuntimeConfig{
				ID:            rt.id,
				PrivateKeyHex: privateKeyHex,
				PrivateKeyEnv: privateKeyEnv,
				Model:         rt.model,
			},
			Active: rt.active.Load(),
		})
	}
	return states
}

func withMockenvLimiter(limiter *GatewayLimiter) gatewayMockOption {
	return func(cfg *gatewayMockConfig) {
		cfg.limiter = limiter
	}
}

func withMockenvSettings(mut func(*GatewaySettings)) gatewayMockOption {
	return func(cfg *gatewayMockConfig) {
		mut(&cfg.settings)
	}
}

func (rt *gatewayMockRuntime) runtime(t *testing.T) *devshardRuntime {
	t.Helper()
	if rt.id == "" {
		t.Fatal("mock runtime id is required")
	}
	if rt.model == "" {
		rt.model = mockenvDefaultModel
	}
	active := rt.active
	if rt.handler == nil {
		active = true
	}
	handler := rt.handler
	if handler == nil {
		handler = func(w http.ResponseWriter, r *http.Request) {
			writeMockenvChatJSON(w, rt.id, rt.model)
		}
	}
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt.calls.Add(1)
		handler(w, r)
	})
	devshard := &devshardRuntime{
		id:      rt.id,
		model:   rt.model,
		handler: wrapped,
	}
	devshard.active.Store(active)
	devshard.activeConfigured = true
	return devshard
}

func (env *gatewayMockEnv) postChat(body string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	env.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	for _, opt := range opts {
		opt(req)
	}
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	return rec
}

func (env *gatewayMockEnv) postDirectChat(id, body string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	env.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/devshard/"+id+"/v1/chat/completions", strings.NewReader(body))
	for _, opt := range opts {
		opt(req)
	}
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	return rec
}

func (env *gatewayMockEnv) get(path string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	env.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, opt := range opts {
		opt(req)
	}
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	return rec
}

func withBearer(token string) func(*http.Request) {
	return func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func mockenvChatBody(model, prompt string) string {
	if model == "" {
		model = mockenvDefaultModel
	}
	return fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}]}`, model, prompt)
}

func writeMockenvChatJSON(w http.ResponseWriter, id, model string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{"id":"chatcmpl-%s","model":%q,"choices":[{"message":{"role":"assistant","content":"from %s"}}]}`, id, model, id)
}

func writeMockenvChatSSE(w http.ResponseWriter, id, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-%s\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"delta\":{\"content\":\"from %s\"},\"finish_reason\":null}]}\n\n", id, model, id)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func requireMockenvJSONField(t *testing.T, body *bytes.Buffer, field string, want any) {
	t.Helper()
	var got map[string]any
	require.NoError(t, json.Unmarshal(body.Bytes(), &got))
	require.Equal(t, want, got[field])
}

func readRequestBodyForTest(t *testing.T, r *http.Request) string {
	t.Helper()
	data, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	return string(data)
}
