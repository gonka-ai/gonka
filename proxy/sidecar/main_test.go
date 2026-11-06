package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPolicyReadinessHandler(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		lookup func(context.Context, string) ([]string, error)
		want   int
	}{
		{
			name: "standalone without network gate",
			lookup: func(context.Context, string) ([]string, error) {
				return nil, errors.New("must not be called")
			},
			want: http.StatusOK,
		},
		{
			name: "application network alias resolves",
			host: "proxy-policy-app-network",
			lookup: func(_ context.Context, host string) ([]string, error) {
				if host != "proxy-policy-app-network" {
					t.Fatalf("lookup host = %q", host)
				}
				return []string{"192.0.2.1"}, nil
			},
			want: http.StatusOK,
		},
		{
			name: "application network alias is unavailable",
			host: "proxy-policy-app-network",
			lookup: func(context.Context, string) ([]string, error) {
				return nil, errors.New("dns unavailable")
			},
			want: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			policyReadinessHandler(tt.host, tt.lookup).ServeHTTP(recorder, request)
			if recorder.Code != tt.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.want)
			}
		})
	}
}

func TestExtractChainRPCMethod(t *testing.T) {
	tests := []struct {
		name    string
		request string
		method  string
		ok      bool
	}{
		{
			name:    "path method",
			request: "GET /chain-rpc/status HTTP/1.1",
			method:  "status",
			ok:      true,
		},
		{
			name:    "path method with query",
			request: "GET /chain-rpc/block?height=123 HTTP/1.1",
			method:  "block",
			ok:      true,
		},
		{
			name:    "chain rpc root",
			request: "POST /chain-rpc/ HTTP/1.1",
			method:  "-",
			ok:      true,
		},
		{
			name:    "sanitizes method",
			request: "GET /chain-rpc/a%20b HTTP/1.1",
			method:  "a_b",
			ok:      true,
		},
		{
			name:    "non chain rpc",
			request: "GET /api/v1/models HTTP/1.1",
			ok:      false,
		},
		{
			name:    "malformed request",
			request: "/chain-rpc/status",
			ok:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method, ok := extractChainRPCMethod(tt.request)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if method != tt.method {
				t.Fatalf("method = %q, want %q", method, tt.method)
			}
		})
	}
}

func TestExtractJSONRPCMethods(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		methods   []string
		batchSize int
		ok        bool
	}{
		{
			name:      "single request",
			body:      `{"jsonrpc":"2.0","id":1,"method":"status","params":{}}`,
			methods:   []string{"status"},
			batchSize: 1,
			ok:        true,
		},
		{
			name:      "batch request",
			body:      `[{"jsonrpc":"2.0","id":1,"method":"status"},{"jsonrpc":"2.0","id":2,"method":"block"}]`,
			methods:   []string{"status", "block"},
			batchSize: 2,
			ok:        true,
		},
		{
			name:      "sanitizes method",
			body:      `{"jsonrpc":"2.0","id":1,"method":"bad method"}`,
			methods:   []string{"bad_method"},
			batchSize: 1,
			ok:        true,
		},
		{
			name: "empty body",
			body: "",
			ok:   false,
		},
		{
			name: "invalid json",
			body: "{",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			methods, batchSize, ok := extractJSONRPCMethods([]byte(tt.body))
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if batchSize != tt.batchSize {
				t.Fatalf("batchSize = %d, want %d", batchSize, tt.batchSize)
			}
			if len(methods) != len(tt.methods) {
				t.Fatalf("methods = %v, want %v", methods, tt.methods)
			}
			for i := range methods {
				if methods[i] != tt.methods[i] {
					t.Fatalf("methods = %v, want %v", methods, tt.methods)
				}
			}
		})
	}
}

func TestExtractJSONRPCLogItemsRedactsParams(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"abci_query","params":{"path":"/cosmos.bank.v1beta1.Query/Balance","data":"Ci1pbmZlcmVuY2UxYWJj","height":"123","prove":false}}`)

	items, batchSize, ok := extractJSONRPCLogItems(body)
	if !ok {
		t.Fatal("expected JSON-RPC log item")
	}
	if batchSize != 1 {
		t.Fatalf("batchSize = %d, want 1", batchSize)
	}
	if len(items) != 1 {
		t.Fatalf("items = %v, want one item", items)
	}
	if items[0].Method != "abci_query" {
		t.Fatalf("method = %q, want abci_query", items[0].Method)
	}

	wantParts := []string{
		"path=/cosmos.bank.v1beta1.Query/Balance",
		"data_len=20",
		"data_sha256=",
		"height=123",
		"prove=false",
	}
	for _, part := range wantParts {
		if !strings.Contains(items[0].Params, part) {
			t.Fatalf("params = %q, missing %q", items[0].Params, part)
		}
	}
	if strings.Contains(items[0].Params, "Ci1pbmZlcmVuY2UxYWJj") {
		t.Fatalf("params leaked raw data: %q", items[0].Params)
	}
}

func TestExtractJSONRPCLogItemsRedactsTxPayload(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"broadcast_tx_sync","params":{"tx":"signed-transaction-with-signature"}}`)

	items, _, ok := extractJSONRPCLogItems(body)
	if !ok {
		t.Fatal("expected JSON-RPC log item")
	}
	if items[0].Params == "" || !strings.Contains(items[0].Params, "tx_len=33") || !strings.Contains(items[0].Params, "tx_sha256=") {
		t.Fatalf("params = %q, want redacted tx summary", items[0].Params)
	}
	if strings.Contains(items[0].Params, "signed-transaction") || strings.Contains(items[0].Params, "signature") {
		t.Fatalf("params leaked tx payload: %q", items[0].Params)
	}
}

func fakeHostLookup(answers map[string][]net.IP) hostLookupFunc {
	return func(_ context.Context, host string) ([]net.IP, error) {
		if ips, ok := answers[host]; ok {
			return ips, nil
		}
		return nil, errors.New("lookup failed")
	}
}

func TestResolveParticipantIPsReusesLastKnownOnLookupFailure(t *testing.T) {
	now := time.Now()
	participants := []Participant{
		{InferenceUrl: "http://8.8.8.8:8000"},
		{InferenceUrl: "http://host-a.example:8000"},
	}
	resolvedAt := now.Add(-time.Hour)
	lastKnown := map[string]hostResolution{"host-a.example": {ips: []net.IP{net.ParseIP("1.1.1.1")}, resolvedAt: resolvedAt}}

	allowed, failures, resolved := resolveParticipantIPs(participants, fakeHostLookup(nil), lastKnown, now)

	if !reflect.DeepEqual(allowed, []string{"1.1.1.1", "8.8.8.8"}) {
		t.Fatalf("unexpected allowed IPs: %v", allowed)
	}
	if failures != 1 {
		t.Fatalf("expected 1 failure, got %d", failures)
	}
	if !resolved["host-a.example"].resolvedAt.Equal(resolvedAt) {
		t.Fatalf("reusing last known IPs must not refresh their age: %v", resolved)
	}
}

func TestResolveParticipantIPsDropsLastKnownAfterGracePeriod(t *testing.T) {
	now := time.Now()
	participants := []Participant{
		{InferenceUrl: "http://8.8.8.8:8000"},
		{InferenceUrl: "http://host-a.example:8000"},
	}
	lastKnown := map[string]hostResolution{"host-a.example": {
		ips:        []net.IP{net.ParseIP("1.1.1.1")},
		resolvedAt: now.Add(-lastKnownIPGracePeriod - time.Second),
	}}

	allowed, failures, resolved := resolveParticipantIPs(participants, fakeHostLookup(nil), lastKnown, now)

	if !reflect.DeepEqual(allowed, []string{"8.8.8.8"}) {
		t.Fatalf("expired IP must not stay whitelisted: %v", allowed)
	}
	if failures != 1 {
		t.Fatalf("expected 1 failure, got %d", failures)
	}
	if _, ok := resolved["host-a.example"]; ok {
		t.Fatalf("expired host must be forgotten: %v", resolved)
	}
}

func TestResolveParticipantIPsMatchesHostnameFormsAcrossSyncs(t *testing.T) {
	now := time.Now()
	lastKnown := map[string]hostResolution{"host-a.example": {ips: []net.IP{net.ParseIP("1.1.1.1")}, resolvedAt: now}}

	for _, url := range []string{"http://HOST-A.example:8000", "http://host-a.example.:8000"} {
		allowed, _, _ := resolveParticipantIPs([]Participant{{InferenceUrl: url}}, fakeHostLookup(nil), lastKnown, now)
		if !reflect.DeepEqual(allowed, []string{"1.1.1.1"}) {
			t.Fatalf("%s must reuse the last known IPs, got %v", url, allowed)
		}
	}
}

func TestResolveParticipantIPsDropsDepartedHosts(t *testing.T) {
	now := time.Now()
	participants := []Participant{{InferenceUrl: "http://host-b.example:8000"}}
	lastKnown := map[string]hostResolution{"host-a.example": {ips: []net.IP{net.ParseIP("1.1.1.1")}, resolvedAt: now}}
	lookup := fakeHostLookup(map[string][]net.IP{"host-b.example": {net.ParseIP("9.9.9.9")}})

	allowed, failures, resolved := resolveParticipantIPs(participants, lookup, lastKnown, now)

	if !reflect.DeepEqual(allowed, []string{"9.9.9.9"}) {
		t.Fatalf("unexpected allowed IPs: %v", allowed)
	}
	if failures != 0 {
		t.Fatalf("expected no failures, got %d", failures)
	}
	if _, ok := resolved["host-a.example"]; ok {
		t.Fatalf("departed host must not be kept: %v", resolved)
	}
}

func TestResolveParticipantIPsNewAnswerReplacesLastKnown(t *testing.T) {
	now := time.Now()
	participants := []Participant{{InferenceUrl: "http://host-a.example:8000"}}
	lastKnown := map[string]hostResolution{"host-a.example": {ips: []net.IP{net.ParseIP("1.1.1.1")}, resolvedAt: now.Add(-time.Hour)}}
	lookup := fakeHostLookup(map[string][]net.IP{"host-a.example": {net.ParseIP("2.2.2.2")}})

	allowed, _, resolved := resolveParticipantIPs(participants, lookup, lastKnown, now)

	if !reflect.DeepEqual(allowed, []string{"2.2.2.2"}) {
		t.Fatalf("unexpected allowed IPs: %v", allowed)
	}
	if !resolved["host-a.example"].resolvedAt.Equal(now) {
		t.Fatalf("a fresh answer must reset the age: %v", resolved)
	}
}

func TestResolveParticipantIPsHostWithoutHistoryDoesNotBlockOthers(t *testing.T) {
	participants := []Participant{
		{InferenceUrl: "http://8.8.8.8:8000"},
		{InferenceUrl: "http://host-a.example:8000"},
	}

	allowed, failures, resolved := resolveParticipantIPs(participants, fakeHostLookup(nil), map[string]hostResolution{}, time.Now())

	if !reflect.DeepEqual(allowed, []string{"8.8.8.8"}) {
		t.Fatalf("unexpected allowed IPs: %v", allowed)
	}
	if failures != 1 {
		t.Fatalf("expected 1 failure, got %d", failures)
	}
	if len(resolved) != 0 {
		t.Fatalf("expected nothing stored for an unresolved host: %v", resolved)
	}
}

func TestResolveParticipantIPsAllPrivateIsNotAFailure(t *testing.T) {
	participants := []Participant{{InferenceUrl: "http://10.1.2.3:8080"}}

	allowed, failures, _ := resolveParticipantIPs(participants, fakeHostLookup(nil), map[string]hostResolution{}, time.Now())

	if len(allowed) != 0 || failures != 0 {
		t.Fatalf("private literal must yield no IPs and no failure: allowed=%v failures=%d", allowed, failures)
	}
}

func TestResolveParticipantIPsLooksUpOriginalHostname(t *testing.T) {
	var looked []string
	var mu sync.Mutex
	lookup := func(_ context.Context, host string) ([]net.IP, error) {
		mu.Lock()
		defer mu.Unlock()
		looked = append(looked, host)
		return []net.IP{net.ParseIP("1.1.1.1")}, nil
	}

	_, _, resolved := resolveParticipantIPs([]Participant{{InferenceUrl: "http://HOST-A.example.:8000"}}, lookup, map[string]hostResolution{}, time.Now())

	if !reflect.DeepEqual(looked, []string{"HOST-A.example."}) {
		t.Fatalf("DNS must see the hostname as configured, got %v", looked)
	}
	if _, ok := resolved["host-a.example"]; !ok {
		t.Fatalf("history must use the canonical key: %v", resolved)
	}
}

func TestSyncWhitelistRevokesWhenHistoryExpired(t *testing.T) {
	const body = `{"active_participants":{"participants":[{"inference_url":"http://host-a.invalid:8000"}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	oldURL, oldHosts := ApiUrl, lastResolvedHosts
	ApiUrl = srv.URL
	lastResolvedHosts = map[string]hostResolution{"host-a.invalid": {
		ips:        []net.IP{net.ParseIP("1.1.1.1")},
		resolvedAt: time.Now().Add(-lastKnownIPGracePeriod - time.Hour),
	}}
	defer func() { ApiUrl, lastResolvedHosts = oldURL, oldHosts }()

	err := syncWhitelist()
	if err != nil && strings.Contains(err.Error(), "preserving existing whitelist state") {
		t.Fatalf("expired history must not preserve the installed whitelist: %v", err)
	}
}

func TestSyncWhitelistKeepsStateWhenNothingResolves(t *testing.T) {
	const body = `{"active_participants":{"participants":[{"inference_url":"http://host-a.invalid:8000"}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	oldURL, oldHosts := ApiUrl, lastResolvedHosts
	ApiUrl, lastResolvedHosts = srv.URL, map[string]hostResolution{}
	defer func() { ApiUrl, lastResolvedHosts = oldURL, oldHosts }()

	err := syncWhitelist()
	if err == nil || !strings.Contains(err.Error(), "preserving existing whitelist state") {
		t.Fatalf("expected a preservation error, got: %v", err)
	}
}
