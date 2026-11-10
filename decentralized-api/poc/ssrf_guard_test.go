package poc

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"common/httpguard"
)

// resolveToLoopback returns a client from NewProofClient whose DNS always
// answers 127.0.0.1, modelling a participant that registered a public hostname
// which resolves (or later rebinds) to a private address.
func resolveToLoopback(t *testing.T) *http.Client {
	t.Helper()
	client := NewProofClient(nil, DefaultProofClientConfig()).httpClient
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	inner := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		return inner(ctx, network, net.JoinHostPort("127.0.0.1", port))
	}
	return client
}

func requireBlocked(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected the dial to be blocked, got nil error")
	}
	if !strings.Contains(err.Error(), "ssrf guard") {
		t.Fatalf("expected an ssrf guard error, got %v", err)
	}
}

// The PoC proof URL comes from the validatee's on-chain InferenceUrl, so the
// client NewProofClient hands to FetchAndVerifyProofs must fail the dial when
// that URL resolves to a private target. Registration-time validation cannot
// catch this (no DNS in ValidateBasic), so the dial is the enforcement point.
func TestProofClientBlocksPrivateDialTargets(t *testing.T) {
	httpguard.SetAllowPrivate(false)

	for _, target := range []string{
		"http://127.0.0.1/v1/poc/proofs",
		"http://169.254.169.254/v1/poc/proofs",
		"http://10.0.0.1/v1/poc/proofs",
		"http://192.168.1.1/v1/poc/proofs",
		"http://[::1]/v1/poc/proofs",
	} {
		t.Run(target, func(t *testing.T) {
			client := NewProofClient(nil, DefaultProofClientConfig()).httpClient
			_, err := client.Get(target)
			requireBlocked(t, err)
		})
	}
}

// A hostname resolving to loopback is the rebinding case the guard exists for:
// the request must never reach the server behind the private address.
func TestProofClientBlocksHostnameResolvingToLoopback(t *testing.T) {
	httpguard.SetAllowPrivate(false)

	var reached bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}

	client := resolveToLoopback(t)
	_, err = client.Get("http://ssrf.attacker.tld:" + port + "/v1/poc/proofs")
	requireBlocked(t, err)
	if reached {
		t.Fatal("guard let the proof request through to the loopback server")
	}
}

// Local dev / docker-compose / e2e register docker-internal hostnames that
// resolve to private IPs, so DAPI_ALLOW_PRIVATE_ADDRESSES must re-open the dial.
// Without this, PoC proof retrieval would fail closed on every local net.
func TestProofClientAllowsPrivateWhenToggled(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	t.Cleanup(func() { httpguard.SetAllowPrivate(false) })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	client := NewProofClient(nil, ProofClientConfig{Timeout: 5 * time.Second}).httpClient
	resp, err := client.Get(server.URL + "/v1/poc/proofs")
	if err != nil {
		t.Fatalf("expected the dial to succeed with the guard disabled, got %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

// Redirect refusal is defense in depth: DialControl already re-checks each hop,
// but a public validatee must not be able to bounce the fetch at all. The caller
// sees the 3xx as a non-200 status instead of following it.
func TestProofClientRefusesRedirects(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	t.Cleanup(func() { httpguard.SetAllowPrivate(false) })

	var followed bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	client := NewProofClient(nil, ProofClientConfig{Timeout: 5 * time.Second}).httpClient
	resp, err := client.Get(redirector.URL + "/v1/poc/proofs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected the 302 to surface unfollowed, got %d", resp.StatusCode)
	}
	if followed {
		t.Fatal("client followed the redirect")
	}
}
