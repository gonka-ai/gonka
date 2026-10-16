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

const (
	proxyComposeFileName = "docker-compose.proxy.yml"
	grpcExhaustedFile    = "grpc-exhausted.http"
)

// ProxyOverlayFromEnv is §9.1. Default citest leaves it unset and peers dial
// versiond-router:8080. The overlay rerun sets TESTENV_PROXY_OVERLAY=1, and
// gencompose then makes http://proxy:8080 the pooled InferenceUrl.
func ProxyOverlayFromEnv() bool {
	return config.ProxyOverlayFromEnv()
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

// PrepareProxyOverlay copies the public-proxy overlay into the stack workdir.
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

	// The overlay compose file is written into the stack workdir. A
	// ../../proxy-router mount is correct only while that file stays in
	// testenv/. From the workdir it misses the repo, HAProxy exits, and
	// the proxy port is never published. Keep the error file beside the
	// copied cfg.
	exhausted, err := os.ReadFile(filepath.Join(s.TestenvDir, "..", "..", "proxy-router", grpcExhaustedFile))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dstCfg, grpcExhaustedFile), exhausted, 0o644))

	srcCompose, err := os.ReadFile(filepath.Join(s.TestenvDir, proxyComposeFileName))
	require.NoError(t, err)
	rewritten := rewriteProxyCompose(string(srcCompose))
	require.Contains(t, rewritten, "./proxy/"+grpcExhaustedFile)
	require.NoError(t, os.WriteFile(filepath.Join(s.WorkDir, proxyComposeFileName), []byte(rewritten), 0o644))
}

func rewriteProxyCompose(src string) string {
	portRe := regexp.MustCompile(`(?m)^(\s*-\s*")127\.0\.0\.1:[0-9]+:([0-9]+)(".*)$`)
	out := portRe.ReplaceAllString(src, `${1}127.0.0.1::${2}${3}`)
	return strings.ReplaceAll(out, "../../proxy-router/"+grpcExhaustedFile, "./proxy/"+grpcExhaustedFile)
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

// rewriteRPCProxyForTest keeps the public TCP hand-off, the production
// ACLs / tables / X-Real-IP, and points the rpc_h2 backend at a local
// server. The test fixture drops docker DNS and lowers path-zone ceilings.
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
		"http-request return status 200 errorfile /etc/haproxy/grpc-exhausted.http if is_native_grpc is_attach { sc_http_req_rate(1) gt 100 }",
		fmt.Sprintf("http-request return status 200 errorfile /etc/haproxy/grpc-exhausted.http if is_native_grpc is_attach { sc_http_req_rate(1) gt %d }", lim.AttachRate), 1)
	out = strings.Replace(out,
		"http-request deny deny_status 429 if is_attach { sc_http_req_rate(1) gt 100 }",
		fmt.Sprintf("http-request deny deny_status 429 if is_attach { sc_http_req_rate(1) gt %d }", lim.AttachRate), 1)
	out = strings.Replace(out,
		"http-request return status 200 errorfile /etc/haproxy/grpc-exhausted.http if is_native_grpc is_chat { sc_http_req_rate(1) gt 2000 }",
		fmt.Sprintf("http-request return status 200 errorfile /etc/haproxy/grpc-exhausted.http if is_native_grpc is_chat { sc_http_req_rate(1) gt %d }", lim.ChatRate), 1)
	out = strings.Replace(out,
		"http-request deny deny_status 429 if is_chat { sc_http_req_rate(1) gt 2000 }",
		fmt.Sprintf("http-request deny deny_status 429 if is_chat { sc_http_req_rate(1) gt %d }", lim.ChatRate), 1)
	out = strings.Replace(out,
		"http-request return status 200 errorfile /etc/haproxy/grpc-exhausted.http if is_native_grpc is_diffs { sc_http_req_rate(1) gt 400 }",
		fmt.Sprintf("http-request return status 200 errorfile /etc/haproxy/grpc-exhausted.http if is_native_grpc is_diffs { sc_http_req_rate(1) gt %d }", lim.DiffsRate), 1)
	out = strings.Replace(out,
		"http-request deny deny_status 429 if is_diffs { sc_http_req_rate(1) gt 400 }",
		fmt.Sprintf("http-request deny deny_status 429 if is_diffs { sc_http_req_rate(1) gt %d }", lim.DiffsRate), 1)
	out = strings.Replace(out,
		"server router versiond-router:8080 proto h2 resolvers docker resolve-prefer ipv4 init-addr last,libc,none",
		"server router "+backend, 1)
	return out
}

// RejectProxyH2 rewrites the overlay HAProxy file in a booted stack so the
// public port resets a connection that opens with the HTTP/2 preface
// instead of handing it to rpc_h2. The reject takes the place of the
// preface accept, so it is evaluated before any accept ends the
// inspection; the hand-off rule below it is then never reached. JSON
// passthrough is unchanged. That is a public port whose wrapping does not
// speak HTTP/2: peers must fall back to Connect over HTTP/1.1 through the
// same port. Recreate proxy afterwards.
func RejectProxyH2(t *testing.T, s *Stack) {
	t.Helper()
	path := filepath.Join(s.WorkDir, "proxy", "haproxy-rpc.cfg")
	text, err := os.ReadFile(path)
	require.NoError(t, err)
	const accept = "    tcp-request content accept if peer_rpc_h2c\n"
	const route = "    use_backend rpc_h2c_in if peer_rpc_h2c\n"
	require.Contains(t, string(text), accept)
	require.Contains(t, string(text), route)
	out := strings.Replace(string(text), accept, "    tcp-request content reject if peer_rpc_h2c\n", 1)
	out = strings.Replace(out, route, "", 1)
	require.NoError(t, os.WriteFile(path, []byte(out), 0o644))
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
	out = replaceProxyRate(out, `http-request return status 200 errorfile /etc/haproxy/grpc-exhausted\.http if is_native_grpc is_attach \{ sc_http_req_rate\(1\) gt \d+ \}`,
		fmt.Sprintf("http-request return status 200 errorfile /etc/haproxy/grpc-exhausted.http if is_native_grpc is_attach { sc_http_req_rate(1) gt %d }", lim.AttachRate))
	out = replaceProxyRate(out, `http-request deny deny_status 429 if is_attach \{ sc_http_req_rate\(1\) gt \d+ \}`,
		fmt.Sprintf("http-request deny deny_status 429 if is_attach { sc_http_req_rate(1) gt %d }", lim.AttachRate))
	out = replaceProxyRate(out, `http-request return status 200 errorfile /etc/haproxy/grpc-exhausted\.http if is_native_grpc is_chat \{ sc_http_req_rate\(1\) gt \d+ \}`,
		fmt.Sprintf("http-request return status 200 errorfile /etc/haproxy/grpc-exhausted.http if is_native_grpc is_chat { sc_http_req_rate(1) gt %d }", lim.ChatRate))
	out = replaceProxyRate(out, `http-request deny deny_status 429 if is_chat \{ sc_http_req_rate\(1\) gt \d+ \}`,
		fmt.Sprintf("http-request deny deny_status 429 if is_chat { sc_http_req_rate(1) gt %d }", lim.ChatRate))
	out = replaceProxyRate(out, `http-request return status 200 errorfile /etc/haproxy/grpc-exhausted\.http if is_native_grpc is_diffs \{ sc_http_req_rate\(1\) gt \d+ \}`,
		fmt.Sprintf("http-request return status 200 errorfile /etc/haproxy/grpc-exhausted.http if is_native_grpc is_diffs { sc_http_req_rate(1) gt %d }", lim.DiffsRate))
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

// ProxyH2HTTP is the host-published overlay proxy URL. Peer RPC (HTTP/2
// preface) and JSON share it, as they share join's public port.
func (s *Stack) ProxyH2HTTP(t *testing.T) string {
	t.Helper()
	return "http://" + s.composePublishedAddr(t, config.DefaultProxyService, config.DefaultProxyPort)
}

// RouterH2HTTP is the host-published versiond-router data URL. The router
// speaks prior-knowledge HTTP/2 on the same :8080 that serves JSON.
func (s *Stack) RouterH2HTTP(t *testing.T) string {
	t.Helper()
	return "http://" + s.composePublishedAddr(t, "versiond-router", config.DefaultVersiondRouterPort)
}
