package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

const proxyComposeFileName = "docker-compose.proxy.yml"

// ProxyOverlayFromEnv is §9.1. Default citest leaves it unset and stays on
// versiond-router:8080. The HTTP/2 rerun sets TESTENV_PROXY_OVERLAY=1.
func ProxyOverlayFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TESTENV_PROXY_OVERLAY"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// ensureProxyOverlay attaches docker-compose.proxy.yml when §9.1 requested it.
// BootProxyOverlayStack already prepared the file; do not copy it twice.
func (s *Stack) ensureProxyOverlay(t *testing.T) {
	t.Helper()
	if s == nil || s.ProxyOverlay || !ProxyOverlayFromEnv() {
		return
	}
	s.PrepareProxyOverlay(t)
	pinProxyOverlayIP(t, s, s.LoadConfig(t))
}

// PrepareProxyOverlay copies the RPC/h2 overlay into the stack workdir.
// Default citest (RunGencompose / Up) must not call this unless
// TESTENV_PROXY_OVERLAY is set.
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

// pinProxyOverlayIP moves the overlay's static address onto this stack's
// subnet. 172.30.0.70 is the default; a non-default base_ip must follow.
func pinProxyOverlayIP(t *testing.T, s *Stack, cfg *config.File) {
	t.Helper()
	base := "172.30.0"
	if cfg != nil && cfg.Network.BaseIP != "" {
		base = cfg.Network.BaseIP
	}
	path := filepath.Join(s.WorkDir, proxyComposeFileName)
	text, err := os.ReadFile(path)
	require.NoError(t, err)
	next := strings.ReplaceAll(string(text), "172.30.0.70", base+".70")
	require.Contains(t, next, "ipv4_address: "+base+".70")
	require.NoError(t, os.WriteFile(path, []byte(next), 0o644))
}

// rpcProxyTestLimits are stick-table ceilings for a one-off HAProxy
// fixture. Production haproxy-rpc.cfg stays high (R8). These exist so
// GetDiffs / Attach floods finish in one test.
type rpcProxyTestLimits struct {
	ConnRate   int
	AttachRate int
	DiffsRate  int
	ChatRate   int
}

func defaultRPCProxyTestLimits() rpcProxyTestLimits {
	return rpcProxyTestLimits{ConnRate: 50, AttachRate: 2, DiffsRate: 3, ChatRate: 50}
}

// rewriteRPCProxyForTest keeps production ACLs / tables / X-Real-IP and
// points the backend at a local server. Bind stays proto h2. The test
// fixture drops docker DNS and lowers path-zone ceilings.
func rewriteRPCProxyForTest(src, backend string, lim rpcProxyTestLimits) string {
	if lim.ConnRate <= 0 {
		lim.ConnRate = 50
	}
	if lim.AttachRate <= 0 {
		lim.AttachRate = 2
	}
	if lim.DiffsRate <= 0 {
		lim.DiffsRate = 3
	}
	if lim.ChatRate <= 0 {
		lim.ChatRate = 50
	}
	out := src
	out = strings.Replace(out,
		"tcp-request connection reject if { sc_conn_rate(0) gt 200 }",
		fmt.Sprintf("tcp-request connection reject if { sc_conn_rate(0) gt %d }", lim.ConnRate), 1)
	out = strings.Replace(out,
		"http-request deny deny_status 429 if is_attach { sc_http_req_rate(1) gt 100 }",
		fmt.Sprintf("http-request deny deny_status 429 if is_attach { sc_http_req_rate(1) gt %d }", lim.AttachRate), 1)
	out = strings.Replace(out,
		"http-request deny deny_status 429 if is_chat { sc_http_req_rate(1) gt 500 }",
		fmt.Sprintf("http-request deny deny_status 429 if is_chat { sc_http_req_rate(1) gt %d }", lim.ChatRate), 1)
	out = strings.Replace(out,
		"http-request deny deny_status 429 if is_diffs { sc_http_req_rate(1) gt 400 }",
		fmt.Sprintf("http-request deny deny_status 429 if is_diffs { sc_http_req_rate(1) gt %d }", lim.DiffsRate), 1)
	out = strings.Replace(out,
		"server router versiond-router:8081 proto h2 resolvers docker resolve-prefer ipv4 init-addr last,libc,none",
		"server router "+backend, 1)
	return out
}

// LowerProxyRPCRates rewrites the overlay HAProxy file in a booted stack.
// Production ceilings stay in the source tree. Recreate proxy afterwards.
// Zero values fall back to a one-off flood (conn 3, attach 2, diffs 3, chat 50).
func LowerProxyRPCRates(t *testing.T, s *Stack, connRate, attachRate, diffsRate, chatRate int) {
	lim := rpcProxyTestLimits{ConnRate: connRate, AttachRate: attachRate, DiffsRate: diffsRate, ChatRate: chatRate}
	t.Helper()
	if lim.ConnRate <= 0 {
		lim.ConnRate = 3
	}
	if lim.AttachRate <= 0 {
		lim.AttachRate = 2
	}
	if lim.DiffsRate <= 0 {
		lim.DiffsRate = 3
	}
	if lim.ChatRate <= 0 {
		lim.ChatRate = 50
	}
	path := filepath.Join(s.WorkDir, "proxy", "haproxy-rpc.cfg")
	text, err := os.ReadFile(path)
	require.NoError(t, err)
	out := string(text)
	out = replaceProxyRate(out, `tcp-request connection reject if \{ sc_conn_rate\(0\) gt \d+ \}`,
		fmt.Sprintf("tcp-request connection reject if { sc_conn_rate(0) gt %d }", lim.ConnRate))
	out = replaceProxyRate(out, `http-request deny deny_status 429 if is_attach \{ sc_http_req_rate\(1\) gt \d+ \}`,
		fmt.Sprintf("http-request deny deny_status 429 if is_attach { sc_http_req_rate(1) gt %d }", lim.AttachRate))
	out = replaceProxyRate(out, `http-request deny deny_status 429 if is_chat \{ sc_http_req_rate\(1\) gt \d+ \}`,
		fmt.Sprintf("http-request deny deny_status 429 if is_chat { sc_http_req_rate(1) gt %d }", lim.ChatRate))
	out = replaceProxyRate(out, `http-request deny deny_status 429 if is_diffs \{ sc_http_req_rate\(1\) gt \d+ \}`,
		fmt.Sprintf("http-request deny deny_status 429 if is_diffs { sc_http_req_rate(1) gt %d }", lim.DiffsRate))
	require.Contains(t, out, fmt.Sprintf("sc_conn_rate(0) gt %d", lim.ConnRate))
	require.Contains(t, out, fmt.Sprintf("is_diffs { sc_http_req_rate(1) gt %d }", lim.DiffsRate))
	require.Contains(t, out, "proto h2 resolvers docker")
	require.NoError(t, os.WriteFile(path, []byte(out), 0o644))
}

func replaceProxyRate(src, pattern, repl string) string {
	re := regexp.MustCompile(pattern)
	if !re.MatchString(src) {
		return src
	}
	return re.ReplaceAllString(src, repl)
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
