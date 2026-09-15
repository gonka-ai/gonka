package harness

import (
	"os"
	"path/filepath"
	"testing"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

func TestRewriteProxyComposeRandomizesHostPort(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", proxyComposeFileName))
	require.NoError(t, err)
	text := string(src)
	require.Regexp(t, `(?m)^  proxy:`, text)
	require.Contains(t, text, `"127.0.0.1:8443:8443"`)
	require.Contains(t, text, "./proxy/haproxy-rpc.cfg")
	require.Contains(t, text, "versiond-router")
	require.NotContains(t, text, "8080:8080")

	got := rewriteProxyCompose(text)
	require.Contains(t, got, `"127.0.0.1::8443"`)
	require.NotContains(t, got, `"127.0.0.1:8443:8443"`)
}

func TestProxyHAProxySpeaksH2ToVersiondRouter(t *testing.T) {
	cfg, err := os.ReadFile(filepath.Join("..", "..", "proxy", "haproxy-rpc.cfg"))
	require.NoError(t, err)
	text := string(cfg)
	require.Contains(t, text, "bind *:8443 proto h2")
	require.Contains(t, text, "versiond-router:8081 proto h2")
	require.Contains(t, text, "X-Real-IP %[src]")
	require.NotContains(t, text, "bind *:8080")
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
	require.NotContains(t, text, "docker compose $(PROXY_OVERLAY)")
}
