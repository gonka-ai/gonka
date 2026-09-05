package httpguard

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// resolveTo builds a client whose dialer is the guarded one but whose DNS
// answers are forced to target, simulating a participant who registered a
// hostname resolving (or rebinding) to an address of their choosing.
func resolveTo(t *testing.T, target string) *http.Client {
	t.Helper()
	client := NewNoRedirectClient(5 * time.Second)
	transport := client.Transport.(*http.Transport)
	dialer := NewDialer()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(target, port))
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

func TestDialControlBlocksPrivateTargets(t *testing.T) {
	SetAllowPrivate(false)

	// Each case is a hostname that resolves to a private target: the literal
	// forms an attacker can register plus the ones a naive string check misses.
	cases := map[string]string{
		"loopback":            "127.0.0.1",
		"loopback_upper_8":    "127.5.6.7",
		"cloud_metadata":      "169.254.169.254",
		"rfc1918_10":          "10.0.0.1",
		"rfc1918_172":         "172.16.0.1",
		"rfc1918_192":         "192.168.1.1",
		"unspecified":         "0.0.0.0",
		"ipv6_unspecified":    "::",
		"ipv6_loopback":       "::1",
		"ipv6_link_local":     "fe80::1",
		"ipv6_ula":            "fc00::1",
		"ipv4_mapped_v6":      "::ffff:10.0.0.1",
		"ipv4_mapped_meta_v6": "::ffff:169.254.169.254",
	}

	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			client := resolveTo(t, target)
			_, err := client.Get("http://ssrf.attacker.tld/")
			requireBlocked(t, err)
		})
	}
}

// A hostname that resolves to loopback is the core of issue #1470: the on-chain
// registration gate cannot reject it (no DNS in ValidateBasic), so the dial is
// where it must fail. Uses a real server to prove the request never lands.
func TestGuardBlocksHostnameResolvingToLoopback(t *testing.T) {
	SetAllowPrivate(false)

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

	client := resolveTo(t, "127.0.0.1")
	_, err = client.Get("http://ssrf.attacker.tld:" + port + "/")
	requireBlocked(t, err)
	if reached {
		t.Fatal("guard let the request through to the loopback server")
	}
}

// Decimal and hex host forms are alternate spellings of 127.0.0.1. The guard
// checks the resolved IP, so the spelling is irrelevant -- this pins that.
func TestGuardBlocksNumericLoopbackSpellings(t *testing.T) {
	SetAllowPrivate(false)

	for _, raw := range []string{"http://2130706433/", "http://0x7f000001/", "http://127.1/"} {
		t.Run(raw, func(t *testing.T) {
			client := NewNoRedirectClient(5 * time.Second)
			_, err := client.Get(raw)
			requireBlocked(t, err)
		})
	}
}

// A public host answering 302 -> 127.0.0.1 must not reach the private target.
// The redirect is refused outright; had it been followed, the new hop's dial
// would hit the guard too.
func TestNoRedirectClientDoesNotFollowRedirectToPrivate(t *testing.T) {
	SetAllowPrivate(true) // let the public-side server on loopback be reachable

	var privateReached bool
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		privateReached = true
	}))
	t.Cleanup(private.Close)

	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL, http.StatusFound)
	}))
	t.Cleanup(public.Close)

	client := NewNoRedirectClient(5 * time.Second)
	resp, err := client.Get(public.URL)
	if err != nil {
		t.Fatalf("expected the 3xx to surface as a response, got %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected the caller to observe 302, got %d", resp.StatusCode)
	}
	if privateReached {
		t.Fatal("client followed the redirect into the private target")
	}
}

// Dev/test environments register docker-internal hostnames that resolve to
// private IPs, so the opt-out has to actually let them through.
func TestAllowPrivateLetsPrivateTargetsThrough(t *testing.T) {
	SetAllowPrivate(true)
	t.Cleanup(func() { SetAllowPrivate(false) })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	client := NewNoRedirectClient(5 * time.Second)
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("allowPrivate should permit the loopback dial, got %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestPublicAddressAllowedInBothModes(t *testing.T) {
	for _, allow := range []bool{false, true} {
		SetAllowPrivate(allow)
		if err := DialControl("tcp", "93.184.216.34:80", nil); err != nil {
			t.Fatalf("public address rejected with allowPrivate=%v: %v", allow, err)
		}
	}
	SetAllowPrivate(false)
}

func TestDialControlFailsClosedOnMalformedAddress(t *testing.T) {
	SetAllowPrivate(false)
	requireBlocked(t, DialControl("tcp", "not-an-address", nil))
	requireBlocked(t, DialControl("tcp", "still.a.hostname:80", nil))
}
