package harness

import (
	"os"
	"path/filepath"
	"testing"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

func TestProxyOverlayFromEnv(t *testing.T) {
	t.Setenv("TESTENV_PROXY_OVERLAY", "")
	require.False(t, ProxyOverlayFromEnv())
	t.Setenv("TESTENV_PROXY_OVERLAY", "1")
	require.True(t, ProxyOverlayFromEnv())
	t.Setenv("TESTENV_PROXY_OVERLAY", "true")
	require.True(t, ProxyOverlayFromEnv())
	t.Setenv("TESTENV_PROXY_OVERLAY", "off")
	require.False(t, ProxyOverlayFromEnv())
}

func TestRewriteProxyComposeRandomizesHostPort(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", proxyComposeFileName))
	require.NoError(t, err)
	text := string(src)
	require.Regexp(t, `(?m)^  proxy:`, text)
	require.Contains(t, text, `"127.0.0.1:8443:8443"`)
	require.Contains(t, text, "./proxy/haproxy-rpc.cfg")
	require.Contains(t, text, "versiond-router")
	require.Contains(t, text, "ipv4_address: 172.30.0.70")
	require.NotContains(t, text, "8080:8080")

	require.Contains(t, text, "../../proxy-router/grpc-exhausted.http")
	_, err = os.Stat(filepath.Join("..", "..", "..", "..", "proxy-router", "grpc-exhausted.http"))
	require.NoError(t, err)

	got := rewriteProxyCompose(text)
	require.Contains(t, got, `"127.0.0.1::8443"`)
	require.NotContains(t, got, `"127.0.0.1:8443:8443"`)
	require.Contains(t, got, "./proxy/grpc-exhausted.http")
	require.NotContains(t, got, "../../proxy-router/grpc-exhausted.http")
}

func TestProxyHAProxySpeaksH2ToVersiondRouter(t *testing.T) {
	cfg, err := os.ReadFile(filepath.Join("..", "..", "proxy", "haproxy-rpc.cfg"))
	require.NoError(t, err)
	text := string(cfg)
	require.Contains(t, text, "bind *:8443 proto h2")
	require.Contains(t, text, "tune.h2.max-concurrent-streams 16384")
	require.Contains(t, text, "versiond-router:8081 proto h2")
	require.Contains(t, text, "http-request del-header X-Real-IP")
	require.Contains(t, text, "X-Real-IP %[src]")
	require.Contains(t, text, "store conn_rate(1s),sess_rate(1s)")
	require.Contains(t, text, "tcp-request connection track-sc0 src")
	require.Contains(t, text, "expose-experimental-directives")
	require.Contains(t, text, "normalize-uri percent-decode-unreserved strict")
	require.Contains(t, text, "normalize-uri percent-to-uppercase")
	require.Contains(t, text, "path_reg ^(/devshard)?/[^/]+/sessions/[^/]+/rpc/.*[%]")
	require.Contains(t, text, "path-strip-dotdot")
	require.Contains(t, text, "deny deny_status 404 unless { path_reg ^(/devshard)?(/[^/]+)?/sessions/[^/]+/rpc/[^/]+/[^/]+$ }")
	require.Contains(t, text, "path_end /devshard.transport.v1.PeerAuthService/Watch")
	require.Contains(t, text, "track-sc1 src table st_rpc_watch")
	require.Contains(t, text, "path_end /devshard.transport.v1.PeerAuthService/Attach")
	require.Contains(t, text, "path_end /devshard.transport.v1.SessionService/Chat")
	require.Contains(t, text, "path_end /devshard.transport.v1.SessionService/GetDiffs")
	require.Contains(t, text, "track-sc1 src table st_rpc_attach")
	require.Contains(t, text, "track-sc1 src table st_rpc_chat")
	require.Contains(t, text, "track-sc1 src table st_rpc_diffs")
	require.NotContains(t, text, "bind *:8080")

	prod, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "proxy-router", "haproxy.cfg.template"))
	require.NoError(t, err)
	require.Contains(t, string(prod), "expose-experimental-directives")
	require.Contains(t, string(prod), "normalize-uri percent-decode-unreserved strict")
	require.Contains(t, string(prod), "normalize-uri percent-to-uppercase")
	require.Contains(t, string(prod), "path_reg ^(/devshard)?/[^/]+/sessions/[^/]+/rpc/.*[%]")
	require.Contains(t, string(prod), "${PUBLIC_PROXY_ACL}")
	require.Contains(t, string(prod), "${PUBLIC_PROXY_EXPECT}")
	require.Contains(t, string(prod), "path-strip-dotdot")
	require.Contains(t, string(prod), "deny deny_status 404 unless { path_reg ^(/devshard)?(/[^/]+)?/sessions/[^/]+/rpc/[^/]+/[^/]+$ }")
	require.Contains(t, string(prod), "st_rpc_watch")
}

func TestVersiondRouterHasNoPerIPZones(t *testing.T) {
	router, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "versiond-router", "haproxy.cfg.template"))
	require.NoError(t, err)
	pool, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "versiond-router", "pool-backend.cfg.template"))
	require.NoError(t, err)
	require.NotContains(t, string(router), "conn_rate")
	require.NotContains(t, string(router), "sess_rate")
	require.NotContains(t, string(pool), "conn_rate")
	require.NotContains(t, string(pool), "sess_rate")
	require.Contains(t, string(router), "Per-IP zones stay on the published hop (proxy), not here.")
	require.Contains(t, string(router), "tune.h2.max-concurrent-streams 16384")
	require.Contains(t, string(router), "bind ${FRONT_BIND_ADDRESS}:${H2_PORT} proto h2")
	require.Contains(t, string(router), "expose-experimental-directives")
	require.Contains(t, string(router), "normalize-uri percent-decode-unreserved strict")
	require.Contains(t, string(router), "normalize-uri percent-to-uppercase")
	require.Contains(t, string(router), "dst_port 8080")
	require.Contains(t, string(router), "var(txn.canonpath) -m reg ^/[^/]+/sessions/[^/]+/rpc/")
	require.NotContains(t, string(router), "path_sub /rpc/")
}

func TestComposeFileArgsDefaultOmitsProxy(t *testing.T) {
	s := &Stack{ComposePath: "docker-compose.yml"}
	require.Equal(t, []string{"-f", "docker-compose.yml"}, s.composeFileArgs())
}

func TestComposeFileArgsProxyOverlay(t *testing.T) {
	dir := t.TempDir()
	overlay := filepath.Join(dir, proxyComposeFileName)
	require.NoError(t, os.WriteFile(overlay, []byte("services:\n  proxy: {}\n"), 0o644))
	s := &Stack{
		WorkDir:      dir,
		ComposePath:  filepath.Join(dir, "docker-compose.yml"),
		ProxyOverlay: true,
	}
	require.Equal(t, []string{
		"-f", s.ComposePath,
		"-f", overlay,
	}, s.composeFileArgs())
}

func TestDefaultInferenceURLStaysRouter8080(t *testing.T) {
	require.Equal(t, "http://versiond-router:8080", config.DefaultEscrowSlotURL)
	require.Equal(t, 8080, config.DefaultVersiondRouterPort)
	require.Equal(t, 8081, config.DefaultVersiondRouterH2Port)
	require.Equal(t, 8443, config.DefaultRPCH2Port)
	require.Equal(t, "proxy", config.DefaultProxyService)
}

func TestMakefileCitestUsesNoProxyCompose(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "COMPOSE_BASE := -f docker-compose.yml")
	require.Contains(t, text, "PROXY_OVERLAY := $(COMPOSE_BASE) -f docker-compose.proxy.yml")
	require.Contains(t, text, "docker compose $(COMPOSE_BASE) pull")
	require.Contains(t, text, "docker compose $(COMPOSE_BASE) build")
	require.Contains(t, text, `docker image inspect "$(TESTENV_VERSIOND_IMAGE)"`)
	require.Contains(t, text, `docker image inspect "$(TESTENV_VERSIOND_ROUTER_IMAGE)"`)
	require.NotContains(t, text, "docker compose $(PROXY_OVERLAY)")
}
