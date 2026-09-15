package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

const proxyComposeFileName = "docker-compose.proxy.yml"

// PrepareProxyOverlay copies the RPC/h2 overlay into the stack workdir.
// Default citest (RunGencompose / Up) must not call this.
func (s *Stack) PrepareProxyOverlay(t *testing.T) {
	t.Helper()
	s.ProxyOverlay = true

	srcCfg := filepath.Join(s.TestenvDir, "proxy")
	dstCfg := filepath.Join(s.WorkDir, "proxy")
	cmd := exec.Command("cp", "-R", srcCfg, dstCfg)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "copy proxy overlay config: %s", out)

	srcCompose, err := os.ReadFile(filepath.Join(s.TestenvDir, proxyComposeFileName))
	require.NoError(t, err)
	rewritten := rewriteProxyCompose(string(srcCompose))
	require.NoError(t, os.WriteFile(filepath.Join(s.WorkDir, proxyComposeFileName), []byte(rewritten), 0o644))
}

func rewriteProxyCompose(src string) string {
	portRe := regexp.MustCompile(`(?m)^(\s*-\s*")127\.0\.0\.1:[0-9]+:([0-9]+)(".*)$`)
	return portRe.ReplaceAllString(src, `${1}127.0.0.1::${2}${3}`)
}

func proxyOverlayPath(s *Stack) string {
	overlay := filepath.Join(s.WorkDir, proxyComposeFileName)
	if _, err := os.Stat(overlay); err != nil {
		return filepath.Join(s.TestenvDir, proxyComposeFileName)
	}
	return overlay
}

// ProxyH2HTTP is the host-published overlay RPC/h2 URL.
func (s *Stack) ProxyH2HTTP(t *testing.T) string {
	t.Helper()
	return "http://" + s.composePublishedAddr(t, config.DefaultProxyService, config.DefaultRPCH2Port)
}
