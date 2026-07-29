package router

import (
	"strings"
	"testing"
	"time"
)

const testTemplate = `${HA_UPSTREAM_SERVERS}
${LEGACY_UPSTREAM_SERVERS}
${VERSIOND_BACKEND_MAP}
${DEVSHARD_HA_HEADER_MAP}
${MAX_BODY_BYTES}
${CONNECT_TIMEOUT_SECONDS}
${STREAM_IDLE_SECONDS}
${UPSTREAM_KEEPALIVE}
`

func TestRenderMarksNonActiveHostDown(t *testing.T) {
	state, err := NewState([]string{"versiond-1", "versiond-2"}, 8080, "versiond-1", []string{"v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.Advance(Transition{
		OperationID: "drain-2", Host: "versiond-2",
		From: HostActive, To: HostDraining, Target: HostOffline,
	}); err != nil {
		t.Fatal(err)
	}
	config, err := Render([]byte(testTemplate), state, DefaultProxyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	text := string(config)
	if !strings.Contains(text, "server versiond-2:8080 resolve max_fails=1 fail_timeout=10s down;") {
		t.Fatalf("rendered config does not mark draining host down:\n%s", text)
	}
	if !strings.Contains(text, "server versiond-1:8080 resolve max_fails=1 fail_timeout=10s;") {
		t.Fatalf("rendered config does not configure HA failover:\n%s", text)
	}
	if !strings.Contains(text, "v1 versiond_legacy;") {
		t.Fatalf("rendered config lost non-HA mapping:\n%s", text)
	}
}

func TestRenderAppliesProxyPolicy(t *testing.T) {
	state, err := NewState([]string{"versiond-1"}, 8080, "versiond-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	config, err := Render([]byte(testTemplate), state, ProxyPolicy{
		MaxBodyBytes:      12 * 1024 * 1024,
		ConnectTimeout:    9 * time.Second,
		StreamIdleTimeout: 31 * time.Minute,
		UpstreamKeepalive: 23,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(config)
	for _, expected := range []string{"12582912", "9", "1860", "23"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("rendered config does not contain %q:\n%s", expected, text)
		}
	}
}

func TestRenderSourceSHATracksAllRenderInputs(t *testing.T) {
	template := []byte(testTemplate)
	defaultSHA := renderSourceSHA(template, ProxyPolicy{})
	if got := renderSourceSHA(template, DefaultProxyPolicy()); got != defaultSHA {
		t.Fatalf("normalized default policy sha = %s, want %s", got, defaultSHA)
	}
	if got := renderSourceSHA(
		append(append([]byte(nil), template...), '#'),
		DefaultProxyPolicy(),
	); got == defaultSHA {
		t.Fatal("template change did not change render source sha")
	}
	policy := DefaultProxyPolicy()
	policy.UpstreamKeepalive++
	if got := renderSourceSHA(template, policy); got == defaultSHA {
		t.Fatal("proxy policy change did not change render source sha")
	}
}
