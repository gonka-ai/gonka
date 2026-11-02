package server

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcserver"
)

func TestRouteInventory_OpsGETsAndRetiredProtocol(t *testing.T) {
	e := echo.New()
	RegisterLazySessionRoutes(e.Group(""), payloadsOnlyResolver{resolves: "1"}, countingBinder{n: new(int)}, nil)

	got := withoutEchoBuiltins(routeKeys(e))
	require.ElementsMatch(t, []string{
		"GET /sessions/:id/diffs",
		"GET /sessions/:id/mempool",
		"GET /sessions/:id/signatures",
		"POST /sessions/:id/chat/completions",
		"POST /sessions/:id/height-sync",
		"POST /sessions/:id/heightsync/repair",
		"POST /sessions/:id/verify-timeout",
		"POST /sessions/:id/verify-error-miss",
		"POST /sessions/:id/challenge-receipt",
		"POST /sessions/:id/gossip/nonce",
		"POST /sessions/:id/gossip/txs",
		"GET /sessions/:id/payloads",
	}, withoutHEAD(got))

	for _, path := range []string{
		"/sessions/1/chat/completions",
		"/sessions/1/height-sync",
		"/sessions/1/heightsync/repair",
		"/sessions/1/verify-timeout",
		"/sessions/1/verify-error-miss",
		"/sessions/1/challenge-receipt",
		"/sessions/1/gossip/nonce",
		"/sessions/1/gossip/txs",
		"/sessions/1/payloads",
	} {
		method := http.MethodPost
		if path == "/sessions/1/payloads" {
			method = http.MethodGet
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		require.Equal(t, http.StatusGone, rec.Code, path)
		require.Equal(t, transport.DevshardErrorHTTPSessionRetired, rec.Header().Get(transport.HeaderDevshardError), path)
	}
}

func TestRouteInventory_RPCMountIsAdditive(t *testing.T) {
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), "host-under-test", rpcserver.PeerAuthConfig{})
	t.Cleanup(auth.Close)
	e := echo.New()
	RegisterLazySessionRoutes(e.Group("/devshard/v2"), payloadsOnlyResolver{resolves: "1"}, countingBinder{n: new(int)}, nil,
		WithPeerRPC(auth, rpcserver.NewSessionHandler(nil)))

	var sawRPC bool
	for _, key := range withoutHEAD(withoutEchoBuiltins(routeKeys(e))) {
		switch {
		case strings.Contains(key, "/sessions/:id/rpc"):
			sawRPC = true
		case strings.HasSuffix(key, "/diffs"), strings.HasSuffix(key, "/mempool"), strings.HasSuffix(key, "/signatures"):
		case strings.Contains(key, "/chat/completions"), strings.Contains(key, "/payloads"),
			strings.Contains(key, "/gossip/"), strings.Contains(key, "/height-sync"),
			strings.Contains(key, "/heightsync/"), strings.Contains(key, "/verify-"),
			strings.Contains(key, "/challenge-receipt"):
		default:
			t.Fatalf("unexpected route %s", key)
		}
	}
	require.True(t, sawRPC, "Connect mount missing")
}

func routeKeys(e *echo.Echo) []string {
	var keys []string
	for _, r := range e.Routes() {
		keys = append(keys, r.Method+" "+r.Path)
	}
	sort.Strings(keys)
	return keys
}

func withoutEchoBuiltins(keys []string) []string {
	var out []string
	for _, key := range keys {
		if strings.Contains(key, "echo_route_not_found") {
			continue
		}
		out = append(out, key)
	}
	return out
}

func withoutHEAD(keys []string) []string {
	var out []string
	for _, key := range keys {
		if len(key) >= 5 && key[:5] == "HEAD " {
			continue
		}
		out = append(out, key)
	}
	return out
}
