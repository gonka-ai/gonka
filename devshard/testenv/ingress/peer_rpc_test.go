//go:build ingressintegration

// These tests render the production entrypoints and run real nginx/HAProxy.
// Only listen ports, DNS membership and filesystem paths are adapted to an
// isolated local fixture. Protocol policy and routing rules are not replaced.
package ingress_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"common/httpguard"
	"connectrpc.com/connect"
	devtest "devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/transport/rpcserver"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

var repo, policyImage string
var sequence atomic.Int32

const rpcPath = "/devshard/v6/sessions/escrow-1/rpc/"
const attach = rpcPath + "devshard.transport.v1.PeerAuthService/Attach"
const chat = rpcPath + "devshard.transport.v1.SessionService/Chat"

func TestMain(m *testing.M) {
	repo, _ = filepath.Abs("../../..")
	policyImage = fmt.Sprintf("gonka-rpc-upgrade-policy:%d", os.Getpid())
	cmd := exec.Command("docker", "build", "-q", "-t", policyImage, filepath.Join(repo, "proxy"))
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build policy: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = exec.Command("docker", "image", "rm", policyImage).Run()
	os.Exit(code)
}

func run(t *testing.T, env []string, command string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s %v\n%s", command, args, out)
	return string(out)
}
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	require.NoError(t, ln.Close())
	return port
}
func write(t *testing.T, path, data string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(data), 0644))
}
func startContainer(t *testing.T, args ...string) string {
	t.Helper()
	name := fmt.Sprintf("rpc-upgrade-%d-%d", os.Getpid(), sequence.Add(1))
	run(t, nil, "docker", append([]string{"run", "-d", "--name", name, "--network", "host", "--user", "0:0"}, args...)...)
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Logf("%s logs:\n%s", name, out)
		}
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
	return name
}
func waitHTTP(t *testing.T, url string, code int) {
	t.Helper()
	c := &http.Client{Timeout: time.Second}
	require.Eventually(t, func() bool {
		r, e := c.Get(url)
		if e != nil {
			return false
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		return r.StatusCode == code
	}, 8*time.Second, 50*time.Millisecond, "%s must return %d", url, code)
}

// origin models versiond. current selects a build with h2c on its listen;
// childH2 selects a child that serves peer RPC over HTTP/2.
type origin struct {
	server         *httptest.Server
	current        atomic.Bool
	childH2        atomic.Bool
	down           atomic.Bool
	active         atomic.Int32
	peak           atomic.Int32
	rpcSeen        atomic.Int32
	hold           chan struct{}
	streamRelease  chan struct{}
	h2CheckStarted chan struct{}
}

func newOrigin(t *testing.T, current bool, h2HealthGate ...<-chan struct{}) *origin {
	t.Helper()
	o := &origin{hold: make(chan struct{}), streamRelease: make(chan struct{}), h2CheckStarted: make(chan struct{}, 1)}
	o.current.Store(current)
	o.childH2.Store(current)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "healthz") || strings.HasSuffix(r.URL.Path, "readyz") {
			if r.ProtoMajor == 2 && len(h2HealthGate) > 0 {
				select {
				case o.h2CheckStarted <- struct{}{}:
				default:
				}
				select {
				case <-h2HealthGate[0]:
				case <-r.Context().Done():
					return
				}
			}
			// down fails only the HTTP/2 health check. Policy nginx probes
			// /readyz over HTTP/1.1; a 503 there leaves policy_http down
			// and HAProxy /readyz never turns 200.
			if o.down.Load() && r.ProtoMajor == 2 {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
			} else if r.URL.Query().Get("peer-rpc") == "1" && o.current.Load() && o.childH2.Load() {
				io.WriteString(w, "peer-rpc-ok\n")
			} else {
				io.WriteString(w, "ready\n")
			}
			return
		}
		if r.Method == "PRI" {
			http.Error(w, "HTTP/1 only", 400)
			return
		}
		if r.ProtoMajor == 2 && !o.childH2.Load() && strings.Contains(r.URL.Path, "/rpc/") {
			http.Error(w, "peer RPC child requires HTTP/1.1 compatibility admission", http.StatusServiceUnavailable)
			return
		}
		if strings.Contains(r.URL.Path, "/rpc/") {
			o.rpcSeen.Add(1)
		}
		w.Header().Set("X-Seen-IP", r.Header.Get("X-Real-IP"))
		w.Header().Set("X-Seen-Proto", strconv.Itoa(r.ProtoMajor))
		if r.Header.Get("X-Hold") == "1" {
			n := o.active.Add(1)
			defer o.active.Add(-1)
			for old := o.peak.Load(); n > old && !o.peak.CompareAndSwap(old, n); old = o.peak.Load() {
			}
			select {
			case <-o.hold:
			case <-r.Context().Done():
				return
			}
		}
		if r.Header.Get("X-Stream") == "1" {
			io.WriteString(w, "first\n")
			w.(http.Flusher).Flush()
			select {
			case <-o.streamRelease:
			case <-r.Context().Done():
				return
			}
			io.WriteString(w, "last\n")
			return
		}
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, "ok\n")
	})
	h2 := h2c.NewHandler(handler, &http2.Server{})
	o.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o.current.Load() {
			h2.ServeHTTP(w, r)
		} else {
			handler.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(o.server.Close)
	t.Cleanup(func() { close(o.hold); close(o.streamRelease) })
	return o
}

type fixture struct {
	httpURL, httpsURL, privateURL, socket string
	roots                                 *x509.CertPool
}

func certificate(t *testing.T, dir string) *x509.CertPool {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	require.NoError(t, err)
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	write(t, filepath.Join(dir, "cert.pem"), string(data))
	write(t, filepath.Join(dir, "private.key"), string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})))
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(data))
	return pool
}

func startProxy(t *testing.T, mode string, o *origin, extraEnv ...string) *fixture {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0755))
	public, tlsPort, private, nginx, nginxTLS, admin, metrics := freePort(t), freePort(t), freePort(t), freePort(t), freePort(t), freePort(t), freePort(t)
	f := &fixture{httpURL: "http://127.0.0.1:" + public, httpsURL: "https://127.0.0.1:" + tlsPort, privateURL: "http://127.0.0.1:" + private, socket: filepath.Join(dir, "stats.sock"), roots: certificate(t, dir)}
	_, port, _ := net.SplitHostPort(o.server.Listener.Addr().String())
	cfg := filepath.Join(dir, "haproxy.cfg")
	env := []string{"HAPROXY_BIN=true", "PROXY_ROUTER_RENDER_ONLY=1", "NGINX_MODE=" + mode, "PROXY_ROUTER_TEMPLATE=" + filepath.Join(repo, "proxy-router/haproxy.cfg.template"), "PROXY_ROUTER_BACKEND_TEMPLATE=" + filepath.Join(repo, "proxy-router/versiond-backend.cfg.template"), "PROXY_ROUTER_OUT=" + cfg, "PROXY_ROUTER_VERSION_MAP=" + filepath.Join(dir, "versions.map"), "PROXY_POLICY_POOL_HOST=127.0.0.1", "PROXY_POLICY_POOL_SLOTS=1", "DEVSHARD_RPC_H2_VERSIOND_HOST=127.0.0.1", "DEVSHARD_RPC_H2_VERSIOND_PORT=" + port, "DEVSHARD_RPC_H2_CERT_DIR=" + dir, "DEVSHARD_RPC_H2_PEM=" + filepath.Join(dir, "rpc.pem"), "PROXY_ROUTER_RPC_FALLBACK_MAXCONN=8", "PROXY_ROUTER_RPC_FALLBACK_MAXQUEUE=8", "PROXY_ROUTER_RPC_FALLBACK_QUEUE_MS=200", "VERSIOND_ROUTING_CATALOG_URL=", "VERSIOND_VERSIONS=", "VERSIOND_NON_HA_VERSIONS=", "VERSIOND_ROUTER_POOL_HOST="}
	run(t, append(env, extraEnv...), filepath.Join(repo, "proxy-router/entrypoint.sh"))
	raw, err := os.ReadFile(cfg)
	require.NoError(t, err)
	s := strings.NewReplacer("bind :80\n", "bind 127.0.0.1:"+public+"\n", "bind :443\n", "bind 127.0.0.1:"+tlsPort+"\n", "bind 0.0.0.0:18081", "bind 127.0.0.1:"+private, "127.0.0.1:8404", "127.0.0.1:"+admin, "127.0.0.1:8405", "127.0.0.1:"+metrics, "127.0.0.1:80 ", "127.0.0.1:"+nginx+" ", "127.0.0.1:443 ", "127.0.0.1:"+nginxTLS+" ", "tcp-check connect port 80 ", "tcp-check connect port "+nginx+" ", "tcp-check connect port 443 ", "tcp-check connect port "+nginxTLS+" ", "/var/run/haproxy/haproxy.sock", f.socket, "/var/run/haproxy/reconciler.sock", filepath.Join(dir, "admin.sock"), "abns@", "abns@"+filepath.Base(dir)+"-", "mode 660", "mode 666").Replace(string(raw))
	for _, value := range extraEnv {
		if seconds, ok := strings.CutPrefix(value, "FIXTURE_CHECK_TIMEOUT_SECONDS="); ok {
			s = strings.ReplaceAll(s, "timeout check 3s", "timeout check "+seconds+"s")
		}
		if rpc, ok := strings.CutPrefix(value, "FIXTURE_ROUTER_RPC_PORT="); ok {
			s = strings.ReplaceAll(s, "127.0.0.1:8081 ", "127.0.0.1:"+rpc+" ")
			s = strings.ReplaceAll(s, "http-check connect port 8081 ", "http-check connect port "+rpc+" ")
		}
	}
	// Numeric membership is known in this fixture; retain all production checks.
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if strings.Contains(line, "127.0.0.1:") {
			lines[i] = strings.ReplaceAll(line, "init-addr none", "init-addr last,libc,none")
		}
	}
	s = strings.Join(lines, "\n")
	write(t, cfg, s)
	startContainer(t, "-v", dir+":"+dir, "-v", filepath.Join(repo, "proxy-router/grpc-exhausted.http")+":/etc/haproxy/grpc-exhausted.http:ro", "-v", filepath.Join(repo, "proxy-router/grpc-unavailable.http")+":/etc/haproxy/grpc-unavailable.http:ro", "haproxy:3.2-alpine", "haproxy", "-W", "-db", "-f", cfg)
	// Render nginx with its real entrypoint, then adapt only isolated ports.
	includes := filepath.Join(dir, "includes")
	require.NoError(t, os.Mkdir(includes, 0755))
	nginxCfg := filepath.Join(dir, "nginx.conf")
	write(t, nginxCfg, "")
	run(t, nil, "docker", "run", "--rm", "--network", "host", "-v", includes+":/etc/nginx/conf.d", "-v", nginxCfg+":/etc/nginx/nginx.conf", "-v", dir+":/etc/nginx/ssl", "-e", "NGINX_MODE="+mode, "-e", "PROXY_PROTOCOL=true", "-e", "PROXY_PROTOCOL_BIND_ADDRESS=127.0.0.1", "-e", "PROXY_PROTOCOL_TRUSTED_FROM=127.0.0.1/32", "-e", "VERSIOND_SERVICE_NAME=127.0.0.1", "-e", "VERSIOND_SERVICE_IS_ABSOLUTE=true", "-e", "VERSIOND_PORT="+port, "-e", "DEVSHARD_RPC_POLICY_HOST=127.0.0.1", "-e", "DISABLE_VALIDATOR_WHITELIST=true", "-e", "DISABLE_FAIL2BAN=true", "-e", "DISABLE_GONKA_API=true", "-e", "DISABLE_CHAIN_API=true", "-e", "DISABLE_CHAIN_RPC=true", "-e", "DISABLE_CHAIN_GRPC=true", policyImage, "true")
	raw, err = os.ReadFile(nginxCfg)
	require.NoError(t, err)
	s = strings.NewReplacer("127.0.0.1:80 ", "127.0.0.1:"+nginx+" ", "127.0.0.1:443 ", "127.0.0.1:"+nginxTLS+" ", "127.0.0.1:8081;", "127.0.0.1:"+freePort(t)+";", "127.0.0.1:18081 ", "127.0.0.1:"+private+" ", "127.0.0.1:8082/", "127.0.0.1:"+port+"/").Replace(string(raw))
	write(t, nginxCfg, s)
	startContainer(t, "--entrypoint", "nginx", "-v", includes+":/etc/nginx/conf.d:ro", "-v", nginxCfg+":/etc/nginx/nginx.conf:ro", "-v", dir+":/etc/nginx/ssl:ro", policyImage, "-g", "daemon off;")
	// Even HTTPS-only configurations have nginx's internal health listener;
	// probe policy availability through HAProxy's own readiness.
	t.Cleanup(func() {
		if t.Failed() {
			for _, file := range []string{cfg, nginxCfg} {
				data, _ := os.ReadFile(file)
				for _, line := range strings.Split(string(data), "\n") {
					if strings.Contains(line, "listen ") || strings.Contains(line, "server-template policy") || strings.Contains(line, "tcp-check") {
						t.Log(line)
					}
				}
			}
			c, e := net.DialTimeout("unix", f.socket, time.Second)
			if e == nil {
				c.SetDeadline(time.Now().Add(time.Second))
				fmt.Fprintln(c, "show stat")
				data, _ := io.ReadAll(c)
				c.Close()
				for _, line := range strings.Split(string(data), "\n") {
					v := strings.Split(line, ",")
					if len(v) > 37 && strings.HasPrefix(v[0], "policy_") {
						t.Logf("%s/%s state=%s check=%s code=%s", v[0], v[1], v[17], v[36], v[37])
					}
				}
			}
		}
	})
	waitHTTP(t, "http://127.0.0.1:"+admin+"/readyz", 200)
	return f
}
func (f *fixture) client(t *testing.T, kind string) *http.Client {
	t.Helper()
	if kind == "h1" || kind == "tls-h1" {
		tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: f.roots}}
		t.Cleanup(tr.CloseIdleConnections)
		return &http.Client{Transport: tr, Timeout: 5 * time.Second}
	}
	tr := &http2.Transport{AllowHTTP: kind == "h2c", TLSClientConfig: &tls.Config{RootCAs: f.roots}}
	tr.DialTLSContext = func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
		if kind == "h2c" {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}
		cfg = cfg.Clone()
		cfg.NextProtos = []string{"gonka-rpc/1"}
		if kind == "tls-nginx-h2" {
			cfg.NextProtos = []string{"h2"}
		}
		d := tls.Dialer{Config: cfg}
		return d.DialContext(ctx, network, addr)
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}
func request(t *testing.T, c *http.Client, url string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", url, strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	require.NoError(t, err)
	return resp
}
func drain(r *http.Response) { io.Copy(io.Discard, r.Body); r.Body.Close() }
func (f *fixture) stats(t *testing.T, command string) string {
	t.Helper()
	return stats(t, f.socket, command)
}
func stats(t *testing.T, socket, command string) string {
	t.Helper()
	c, err := net.DialTimeout("unix", socket, time.Second)
	require.NoError(t, err)
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	fmt.Fprintln(c, command)
	out, err := io.ReadAll(c)
	require.NoError(t, err)
	return string(out)
}

// waitChecked waits until every server of backend has completed a health
// check and is UP (up) or DOWN (!up).
func waitChecked(t *testing.T, socket, backend string, up bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		servers := 0
		for _, line := range strings.Split(stats(t, socket, "show stat"), "\n") {
			v := strings.Split(line, ",")
			if len(v) <= 36 || v[0] != backend || v[1] == "BACKEND" || v[1] == "FRONTEND" {
				continue
			}
			servers++
			if v[36] == "" || strings.Contains(v[36], "INI") || strings.HasPrefix(v[17], "UP") != up {
				return false
			}
		}
		return servers > 0
	}, 8*time.Second, 50*time.Millisecond, "%s servers must be checked with up=%v", backend, up)
}

func TestProtocolMatrixAndSharedBudgets(t *testing.T) {
	for _, mode := range []string{"http", "https", "both"} {
		t.Run(mode, func(t *testing.T) {
			o := newOrigin(t, true)
			f := startProxy(t, mode, o)
			kinds := []string{"h1", "h2c"}
			if mode == "https" {
				kinds = nil
				for _, kind := range []string{"h1", "h2c"} {
					req, _ := http.NewRequest("POST", f.httpURL+attach, strings.NewReader("{}"))
					r, err := f.client(t, kind).Do(req)
					if r != nil {
						drain(r)
					}
					require.Error(t, err, "HTTPS-only must reject plaintext %s RPC", kind)
				}
			}
			if mode != "http" {
				kinds = append(kinds, "tls-h1", "tls-h2", "tls-nginx-h2")
			}
			clients := make([]*http.Client, len(kinds))
			urls := make([]string, len(kinds))
			for i, kind := range kinds {
				clients[i] = f.client(t, kind)
				urls[i] = f.httpURL
				if strings.HasPrefix(kind, "tls-") {
					urls[i] = f.httpsURL
				}
			}
			require.Eventually(t, func() bool {
				return strings.Contains(f.stats(t, "show stat"), "rpc_h2_upstream,versiond,") && strings.Contains(f.stats(t, "show stat"), "L7OK")
			}, 5*time.Second, 50*time.Millisecond)
			for i, kind := range kinds {
				t.Run(kind, func(t *testing.T) {
					r := request(t, clients[i], urls[i]+chat, map[string]string{"X-Real-IP": "203.0.113.1"})
					require.Equal(t, 200, r.StatusCode)
					require.Equal(t, "127.0.0.1", r.Header.Get("X-Seen-IP"))
					drain(r)
					r = request(t, clients[i], urls[i]+chat, map[string]string{"X-Stream": "1"})
					line, err := bufio.NewReader(r.Body).ReadString('\n')
					require.NoError(t, err)
					require.Equal(t, "first\n", line)
					// The origin cannot finish until the client observes its first chunk.
					o.streamRelease <- struct{}{}
					drain(r)
				})
			}
			f.stats(t, "clear table st_rpc_attach")
			accepted, limited := 0, 0
			for i := 0; i < 120; i++ {
				j := i % len(kinds)
				r := request(t, clients[j], urls[j]+attach, map[string]string{"X-Real-IP": fmt.Sprintf("203.0.113.%d", i)})
				switch r.StatusCode {
				case 200:
					accepted++
				case 429:
					limited++
				default:
					t.Fatalf("unexpected status %d", r.StatusCode)
				}
				drain(r)
			}
			require.Equal(t, 100, accepted, "alternating transports must share one Attach budget")
			require.Equal(t, 20, limited)
			for i, c := range clients {
				auth := rpcpbconnect.NewPeerAuthServiceClient(c, urls[i]+rpcPath[:len(rpcPath)-1])
				_, err := auth.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{}))
				require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err), "Connect must receive a typed refusal")
				for _, path := range []string{strings.Replace(attach, "/Attach", "/%41ttach", 1), strings.Replace(attach, "escrow-1/rpc", "escrow-1%2Frpc", 1), strings.Replace(attach, "/rpc/", "/rpc/sessions/escrow-1/rpc/", 1), strings.Replace(attach, "/devshard/", "/devshard/devshard/", 1)} {
					r := request(t, c, urls[i]+path, nil)
					require.Contains(t, []int{400, 429}, r.StatusCode, "encoded path must not bypass RPC policy")
					drain(r)
				}
				if kinds[i] == "h2c" || kinds[i] == "tls-h2" {
					grpc := rpcpbconnect.NewPeerAuthServiceClient(c, urls[i]+rpcPath[:len(rpcPath)-1], connect.WithGRPC())
					_, err = grpc.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{}))
					require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err), "native gRPC must receive grpc-status 8")
				}
			}

			// A procedure refusal must not exhaust unrelated Chat or JSON traffic.
			r := request(t, clients[0], urls[0]+chat, nil)
			require.Equal(t, 200, r.StatusCode)
			drain(r)
			r = request(t, clients[0], urls[0]+"/devshard/v6/healthz", nil)
			if kinds[0] == "h2c" || kinds[0] == "tls-h2" {
				require.Equal(t, 404, r.StatusCode)
			} else {
				require.Equal(t, 200, r.StatusCode)
			}
			drain(r)
		})
	}
}

// A versiond without h2c opens the bounded HTTP/1.1 hop.
func TestH2ToLegacyH1BoundedAcrossVersions(t *testing.T) {
	o := newOrigin(t, false)
	f := startProxy(t, "http", o)
	waitChecked(t, f.socket, "rpc_h1_only", true)
	c := f.client(t, "h2c")
	grpc := rpcpbconnect.NewPeerAuthServiceClient(c, f.httpURL+rpcPath[:len(rpcPath)-1], connect.WithGRPC())
	_, err := grpc.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{}))
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err), "H1 child must not receive native gRPC")
	var wg sync.WaitGroup
	var ok, refused atomic.Int32
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := strings.Replace(chat, "/v6/", fmt.Sprintf("/v%d/", i%3), 1)
			req, _ := http.NewRequest("POST", f.httpURL+path, strings.NewReader("{}"))
			req.Header.Set("X-Hold", "1")
			r, e := c.Do(req)
			if e != nil {
				t.Errorf("request: %v", e)
				return
			}
			if r.StatusCode == 200 {
				ok.Add(1)
			} else if r.StatusCode == 503 {
				refused.Add(1)
			} else {
				t.Errorf("status %d", r.StatusCode)
			}
			drain(r)
		}(i)
	}
	require.Eventually(t, func() bool { return o.active.Load() == 8 && refused.Load() >= 52 }, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, int32(8), o.peak.Load(), "one global fallback budget, not eight per version")
	// JSON uses a separate capacity path while all RPC fallback slots are busy.
	r := request(t, f.client(t, "h1"), f.httpURL+"/devshard/v6/healthz", nil)
	require.Equal(t, 200, r.StatusCode)
	drain(r)
	// Runtime queue is empty after its short deadline, even while Watch-like work stays open.
	for _, line := range strings.Split(f.stats(t, "show stat"), "\n") {
		if strings.HasPrefix(line, "rpc_h1_fallback,BACKEND,") {
			require.Equal(t, "0", strings.Split(line, ",")[2])
		}
	}
	for range 8 {
		o.hold <- struct{}{}
	}
	wg.Wait()
	require.Equal(t, int32(8), ok.Load())
	require.Equal(t, int32(52), refused.Load())
}

// Behind a versiond with h2c the HTTP/1.1 hop stays closed. Peer RPC with no
// HTTP/2 server is refused at once, and HTTP/2 RPC to a child without h2c
// gets versiond's own 503. Neither reaches a child.
func TestH2VersiondNeverUsesH1Fallback(t *testing.T) {
	for _, tc := range []struct {
		name          string
		down, childH2 bool
	}{
		{"h2-upstream-down", true, true},
		{"h1-child", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := newOrigin(t, true)
			o.down.Store(tc.down)
			o.childH2.Store(tc.childH2)
			f := startProxy(t, "http", o)
			waitChecked(t, f.socket, "rpc_h1_only", false)
			waitChecked(t, f.socket, "rpc_h2_upstream", !tc.down)
			for _, kind := range []string{"h1", "h2c"} {
				c := f.client(t, kind)
				r := request(t, c, f.httpURL+chat, nil)
				require.Equal(t, 503, r.StatusCode, kind)
				if tc.down {
					require.Equal(t, "1", r.Header.Get("Retry-After"), kind)
				}
				drain(r)
				auth := rpcpbconnect.NewPeerAuthServiceClient(c, f.httpURL+rpcPath[:len(rpcPath)-1])
				_, err := auth.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{}))
				require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err), "%s refusal must stay retryable", kind)
			}
			require.Zero(t, o.rpcSeen.Load(), "peer RPC reached a child")
		})
	}
}

func TestShortHTTP10AndFragmentedH2(t *testing.T) {
	f := startProxy(t, "http", newOrigin(t, true))
	addr := strings.TrimPrefix(f.httpURL, "http://")
	for _, raw := range []string{"GET / HTTP/1.0\r\n\r\n", "OPTIONS / HTTP/1.0\r\n\r\n"} {
		c, err := net.Dial("tcp", addr)
		require.NoError(t, err)
		c.SetDeadline(time.Now().Add(time.Second))
		start := time.Now()
		io.WriteString(c, raw)
		_, err = http.ReadResponse(bufio.NewReader(c), nil)
		c.Close()
		require.NoError(t, err)
		require.Less(t, time.Since(start), time.Second)
	}
	for _, split := range []int{1, 3, 16, 20, 21} {
		c, err := net.Dial("tcp", addr)
		require.NoError(t, err)
		c.SetDeadline(time.Now().Add(time.Second))
		p := []byte(http2.ClientPreface)
		for len(p) > 0 {
			n := min(split, len(p))
			_, err = c.Write(p[:n])
			require.NoError(t, err)
			p = p[n:]
			time.Sleep(time.Millisecond)
		}
		fr := http2.NewFramer(c, c)
		require.NoError(t, fr.WriteSettings())
		frame, err := fr.ReadFrame()
		require.NoError(t, err)
		_, ok := frame.(*http2.SettingsFrame)
		require.True(t, ok)
		c.Close()
	}
	// HTTP-only TLS probes must not consume the cleartext RPC connection table.
	require.Eventually(t, func() bool { return !regexp.MustCompile(`conn_cur=[1-9]`).MatchString(f.stats(t, "show table rpc_h2")) }, time.Second, 10*time.Millisecond)
	f.stats(t, "clear table rpc_h2")
	for range 5 {
		c, _ := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", strings.TrimPrefix(f.httpsURL, "https://"), &tls.Config{RootCAs: f.roots, NextProtos: []string{"gonka-rpc/1"}})
		if c != nil {
			c.Close()
		}
	}
	require.NotRegexp(t, regexp.MustCompile(`conn_rate\(1000\)=[1-9]`), f.stats(t, "show table rpc_h2"))
}

type innerRouter struct{ data, rpc, admin, socket string }

func startInner(t *testing.T, o *origin) innerRouter {
	t.Helper()
	dir := t.TempDir()
	data, rpc, admin, metrics := freePort(t), freePort(t), freePort(t), freePort(t)
	_, port, _ := net.SplitHostPort(o.server.Listener.Addr().String())
	cfg := filepath.Join(dir, "router.cfg")
	env := []string{"HAPROXY_BIN=true", "VERSIOND_ROUTER_RENDER_ONLY=1", "VERSIOND_ROUTER_TEMPLATE=" + filepath.Join(repo, "versiond-router/haproxy.cfg.template"), "VERSIOND_ROUTER_POOL_TEMPLATE=" + filepath.Join(repo, "versiond-router/pool-backend.cfg.template"), "VERSIOND_ROUTER_OUT=" + cfg, "VERSIOND_ROUTER_NON_HA_MAP=" + filepath.Join(dir, "legacy.map"), "VERSIOND_ROUTER_VERSIONS_MAP=" + filepath.Join(dir, "versions.map"), "VERSIOND_POOL_HOST=127.0.0.1", "VERSIOND_PORT=" + port, "VERSIOND_ROUTER_POOL_SLOTS=1", "VERSIOND_VERSIONS=v6", "VERSIOND_NON_HA_VERSIONS=", "VERSIOND_ROUTING_CATALOG_URL=", "VERSIOND_ROUTER_RPC_FALLBACK_MAXCONN=8"}
	run(t, env, filepath.Join(repo, "versiond-router/entrypoint.sh"))
	raw, err := os.ReadFile(cfg)
	require.NoError(t, err)
	s := strings.NewReplacer("bind :8080 ", "bind 127.0.0.1:"+data+" ", "bind :8081 ", "bind 127.0.0.1:"+rpc+" ", "dst_port 8081", "dst_port "+rpc, "bind :8404", "bind 127.0.0.1:"+admin, "bind 127.0.0.1:8404", "bind 127.0.0.1:"+admin, "127.0.0.1:8405", "127.0.0.1:"+metrics, "/var/run/haproxy/", dir+"/", "abns@", "abns@"+strconv.Itoa(os.Getpid())+"-inner-"+data+"-", "init-addr none", "init-addr last,libc,none", "mode 660", "mode 666").Replace(string(raw))
	// With no front bind host the renderer emits only one loopback admin bind.
	write(t, cfg, s)
	startContainer(t, "-v", dir+":"+dir, "haproxy:3.2-alpine", "haproxy", "-W", "-db", "-f", cfg)
	waitHTTP(t, "http://127.0.0.1:"+data+"/v6/healthz", 200)
	return innerRouter{data: data, rpc: rpc, admin: admin, socket: filepath.Join(dir, "haproxy.sock")}
}

func TestMixedVersionUpgradeAndRollback(t *testing.T) {
	o := newOrigin(t, false)
	router := startInner(t, o)
	// The new inner router must keep old versiond's H1 forwarding available,
	// while its RPC data health correctly refuses admission to the H2 pool.
	waitHTTP(t, "http://127.0.0.1:"+router.rpc+"/v6/healthz", 503)
	f := startProxy(t, "http", o, "VERSIOND_ROUTER_POOL_HOST=127.0.0.1", "VERSIOND_ROUTER_FLEET_CAPACITY=1", "VERSIOND_ROUTER_PORT="+router.data, "VERSIOND_ROUTER_ADMIN_PORT="+router.admin, "VERSIOND_VERSIONS=v6", "FIXTURE_ROUTER_RPC_PORT="+router.rpc)
	// The proxy hands HTTP/2 RPC to any router with the :8081 listen; that
	// router's own gate opens its bounded H1 hop to the old versiond.
	waitChecked(t, router.socket, "versiond_h1_only", true)
	waitChecked(t, f.socket, "rpc_h2_routers", true)
	h1, h2 := f.client(t, "h1"), f.client(t, "h2c")
	check := func(want string) {
		t.Helper()
		for _, c := range []*http.Client{h1, h2} {
			r := request(t, c, f.httpURL+chat, nil)
			require.Equal(t, 200, r.StatusCode)
			require.Equal(t, want, r.Header.Get("X-Seen-Proto"))
			drain(r)
		}
	}
	check("1")
	nested := strings.Replace(attach, "/rpc/", "/rpc/sessions/escrow-1/rpc/", 1)
	for _, c := range []*http.Client{h1, h2} {
		r := request(t, c, f.httpURL+nested, nil)
		require.Equal(t, 400, r.StatusCode, "nested RPC must not bypass policy through HA JSON routing")
		drain(r)
	}
	grpc := rpcpbconnect.NewPeerAuthServiceClient(h2, f.httpURL+rpcPath[:len(rpcPath)-1], connect.WithGRPC())
	_, err := grpc.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{}))
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err), "native gRPC must not be translated to H1")
	o.current.Store(true)
	o.childH2.Store(true)
	waitHTTP(t, "http://127.0.0.1:"+router.rpc+"/v6/healthz", 200)
	require.Eventually(t, func() bool {
		req, _ := http.NewRequest("POST", f.httpURL+chat, strings.NewReader("{}"))
		r, e := h2.Do(req)
		if e != nil {
			return false
		}
		defer drain(r)
		return r.StatusCode == 200 && r.Header.Get("X-Seen-Proto") == "2"
	}, 8*time.Second, 50*time.Millisecond, "public H2 admission must recover after versiond upgrade")
	check("2")
	waitChecked(t, router.socket, "versiond_h1_only", false)
	o.current.Store(false)
	waitHTTP(t, "http://127.0.0.1:"+router.rpc+"/v6/healthz", 503)
	require.Eventually(t, func() bool {
		req, _ := http.NewRequest("POST", f.httpURL+chat, strings.NewReader("{}"))
		r, e := h2.Do(req)
		if e != nil {
			return false
		}
		defer drain(r)
		return r.StatusCode == 200 && r.Header.Get("X-Seen-Proto") == "1"
	}, 8*time.Second, 50*time.Millisecond, "partial rollback must recover through bounded H1")
	check("1")
}

// A new inner router can receive H2 from an already-updated public proxy while
// all children are still H1-only. Its own admission must bound that fan-out.
func TestInnerRouterBoundsLegacyFanout(t *testing.T) {
	o := newOrigin(t, false)
	router := startInner(t, o)
	tr := &http2.Transport{AllowHTTP: true, DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
	t.Cleanup(tr.CloseIdleConnections)
	c := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	base := "http://127.0.0.1:" + router.rpc
	waitChecked(t, router.socket, "versiond_h1_only", true)
	var wg sync.WaitGroup
	var ok, refused atomic.Int32
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", base+chat, strings.NewReader("{}"))
			req.Header.Set("X-Hold", "1")
			r, err := c.Do(req)
			if err != nil {
				t.Errorf("request: %v", err)
				return
			}
			defer drain(r)
			switch r.StatusCode {
			case 200:
				ok.Add(1)
			case 503:
				refused.Add(1)
			default:
				t.Errorf("unexpected status %d", r.StatusCode)
			}
		}()
	}
	require.Eventually(t, func() bool { return o.active.Load() == 8 && refused.Load() == 24 }, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, int32(8), o.peak.Load())
	r := request(t, c, base+"/devshard/v6/sessions/test/status", nil)
	require.Equal(t, 200, r.StatusCode, "ordinary JSON must retain capacity")
	drain(r)
	for range 8 {
		o.hold <- struct{}{}
	}
	wg.Wait()
	require.Equal(t, int32(8), ok.Load())
}

func TestRejectInvalidFallbackConfiguration(t *testing.T) {
	for _, router := range []string{"proxy-router", "versiond-router"} {
		prefix := "PROXY_ROUTER"
		if router == "versiond-router" {
			prefix = "VERSIOND_ROUTER"
		}
		for _, field := range []string{"MAXCONN", "MAXQUEUE", "QUEUE_MS"} {
			for _, value := range []string{"0", "-1", "invalid"} {
				t.Run(router+"/"+field+"/"+value, func(t *testing.T) {
					cmd := exec.Command(filepath.Join(repo, router, "entrypoint.sh"))
					cmd.Env = append(os.Environ(), "HAPROXY_BIN=true", "PROXY_ROUTER_BACKEND_TEMPLATE="+filepath.Join(repo, "proxy-router/versiond-backend.cfg.template"), prefix+"_RPC_FALLBACK_"+field+"="+value)
					out, err := cmd.CombinedOutput()
					require.Error(t, err, "invalid budget accepted")
					require.Regexp(t, `(?i)(fallback|positive|numeric|integer)`, string(out), "must reject the budget before reading configuration files")
				})
			}
		}
	}
}

func TestNonHACapabilityCheckPending(t *testing.T) {
	gate := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(gate) })
	o := newOrigin(t, true, gate)
	// Hold the first HTTP/2 check until requests have been observed. A longer
	// check timeout prevents a slow CI startup from accidentally testing a
	// completed failed check instead of the initial, unverified state.
	f := startProxy(t, "http", o, "FIXTURE_CHECK_TIMEOUT_SECONDS=60")
	select {
	case <-o.h2CheckStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP/2 check did not start")
	}
	waitChecked(t, f.socket, "rpc_h1_only", false)
	c := f.client(t, "h2c")
	r := request(t, c, f.httpURL+chat, nil)
	require.Equal(t, 503, r.StatusCode, "an unverified HTTP/2 upstream must not open the HTTP/1.1 hop")
	require.Equal(t, "1", r.Header.Get("Retry-After"))
	drain(r)
	grpc := rpcpbconnect.NewPeerAuthServiceClient(c, f.httpURL+rpcPath[:len(rpcPath)-1], connect.WithGRPC())
	_, err := grpc.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{}))
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err), "unverified H2 backend cannot carry native gRPC")
	require.Zero(t, o.rpcSeen.Load(), "peer RPC reached a child before its check passed")
	release.Do(func() { close(gate) })
	require.Eventually(t, func() bool {
		req, _ := http.NewRequest("POST", f.httpURL+chat, strings.NewReader("{}"))
		r, err := c.Do(req)
		if err != nil {
			return false
		}
		defer drain(r)
		return r.StatusCode == 200 && r.Header.Get("X-Seen-Proto") == "2"
	}, 8*time.Second, 50*time.Millisecond)
}

// The production HTTP/1.1 fallback budget is 512. versiond here is the real
// proxy handler's job: HTTP/2 on its listen, Attach forwarded to a real v6
// child, v4.2 JSON forwarded to an HTTP/1.1 child. More v6 streams than that
// budget stay on the HTTP/2 upstream. They do not take fallback slots and do
// not get the fallback 503.
func TestNonHAV6StreamsStayOffProductionFallback(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	t.Cleanup(func() { httpguard.SetAllowPrivate(false) })
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, rpcserver.PeerAuthConfig{Heartbeat: time.Hour})
	t.Cleanup(auth.Close)
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(nil))
	var childProto atomic.Int32
	child := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		childProto.Store(int32(r.ProtoMajor))
		if i := strings.LastIndex(r.URL.Path, "/rpc/"); i >= 0 {
			u := *r.URL
			u.Path = r.URL.Path[i+len("/rpc"):]
			r = r.Clone(rpcserver.WithEscrowID(r.Context(), "escrow-1"))
			r.URL = &u
		} else {
			r = r.WithContext(rpcserver.WithEscrowID(r.Context(), "escrow-1"))
		}
		mux.ServeHTTP(w, r)
	}), &http2.Server{}))
	t.Cleanup(child.Close)
	var v42 atomic.Int32
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			http.Error(w, "v4.2 is HTTP/1.1", http.StatusHTTPVersionNotSupported)
			return
		}
		v42.Add(1)
		io.WriteString(w, "ok\n")
	}))
	t.Cleanup(legacy.Close)

	o := &origin{hold: make(chan struct{}), streamRelease: make(chan struct{}), h2CheckStarted: make(chan struct{}, 1)}
	o.current.Store(true)
	o.childH2.Store(true)
	childURL, err := url.Parse(child.URL)
	require.NoError(t, err)
	legacyURL, err := url.Parse(legacy.URL)
	require.NoError(t, err)
	toChild := &http2.Transport{AllowHTTP: true, DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
	versiond := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			io.WriteString(w, "ready\n")
			return
		}
		if strings.Contains(r.URL.Path, "/v4.2/") {
			httputil.NewSingleHostReverseProxy(legacyURL).ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-Hold") == "1" {
			w.Header().Set("X-Seen-Proto", strconv.Itoa(r.ProtoMajor))
			o.active.Add(1)
			defer o.active.Add(-1)
			select {
			case <-o.hold:
			case <-r.Context().Done():
			}
			io.WriteString(w, "ok\n")
			return
		}
		if !strings.Contains(r.URL.Path, "/rpc/") {
			http.NotFound(w, r)
			return
		}
		out := r.Clone(r.Context())
		u := *childURL
		u.Path = r.URL.Path
		u.RawQuery = r.URL.RawQuery
		out.URL = &u
		out.RequestURI = ""
		resp, err := toChild.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})
	o.server = httptest.NewServer(h2c.NewHandler(versiond, &http2.Server{}))
	t.Cleanup(o.server.Close)
	t.Cleanup(func() { close(o.hold); close(o.streamRelease) })

	f := startProxy(t, "http", o,
		"PROXY_ROUTER_RPC_FALLBACK_MAXCONN=512",
		"PROXY_ROUTER_RPC_FALLBACK_MAXQUEUE=64",
		"PROXY_ROUTER_RPC_FALLBACK_QUEUE_MS=200")
	waitChecked(t, f.socket, "rpc_h2_upstream", true)
	waitChecked(t, f.socket, "rpc_h1_only", false)
	// slim is the server's configured maxconn. show stat has no maxconn column.
	require.Equal(t, "512", statValue(t, f.stats(t, "show stat"), "rpc_h1_fallback", "local", "slim"))

	pc := transport.NewPeerConn(transport.PeerConnConfig{
		BaseURL:        f.httpURL,
		HostAddress:    hostAddr,
		Signer:         peer,
		DoorEscrowID:   "escrow-1",
		DialSet:        transport.PeerRPCDialSet{InferenceURL: f.httpURL, H2URL: f.httpURL},
		H2ProbeTimeout: time.Second,
		WatchStale:     time.Minute,
	})
	t.Cleanup(pc.Close)
	transport.ResetRPCH2MissCacheForTest()
	transport.ResetRPCH2ClientPoolForTest()
	t.Cleanup(transport.ResetRPCH2MissCacheForTest)
	t.Cleanup(transport.ResetRPCH2ClientPoolForTest)
	pc.Start()
	require.Eventually(t, func() bool { return pc.Ready() && len(pc.LiveToken()) > 0 }, 8*time.Second, 20*time.Millisecond)
	require.True(t, pc.UsingH2(), "Attach reached versiond over HTTP/2")
	require.Equal(t, int32(2), childProto.Load(), "the v6 child saw HTTP/2")

	tr := &http2.Transport{AllowHTTP: true, DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
	t.Cleanup(tr.CloseIdleConnections)
	c := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	const streams = 513
	var wg sync.WaitGroup
	var ok, refused atomic.Int32
	for range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest("POST", f.httpURL+chat, strings.NewReader("{}"))
			if err != nil {
				t.Errorf("request: %v", err)
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Connect-Protocol-Version", "1")
			req.Header.Set("X-Hold", "1")
			r, err := c.Do(req)
			if err != nil {
				t.Errorf("request: %v", err)
				return
			}
			defer drain(r)
			if r.StatusCode == 200 && r.Header.Get("X-Seen-Proto") == "2" {
				ok.Add(1)
				return
			}
			if r.StatusCode == 503 {
				refused.Add(1)
			}
			t.Errorf("status %d proto %q", r.StatusCode, r.Header.Get("X-Seen-Proto"))
		}()
	}
	require.Eventually(t, func() bool { return o.active.Load() == streams }, 15*time.Second, 20*time.Millisecond)
	dump := f.stats(t, "show stat")
	require.Equal(t, "0", statValue(t, dump, "rpc_h1_fallback", "BACKEND", "scur"), "v6 streams took HTTP/1.1 fallback slots")
	h2, err := strconv.Atoi(statValue(t, dump, "rpc_h2_upstream", "BACKEND", "scur"))
	require.NoError(t, err)
	require.Greater(t, h2, 0, "v6 streams are not on the HTTP/2 upstream")
	jsonReq := request(t, f.client(t, "h1"), f.httpURL+"/devshard/v4.2/chat/completions", nil)
	require.Equal(t, 200, jsonReq.StatusCode, "v4.2 JSON must keep moving while v6 streams are open")
	drain(jsonReq)
	require.Greater(t, v42.Load(), int32(0), "v4.2 JSON must reach the HTTP/1.1 child")
	require.Zero(t, refused.Load())
	for range streams {
		o.hold <- struct{}{}
	}
	wg.Wait()
	require.Equal(t, int32(streams), ok.Load())
}

func statValue(t *testing.T, dump, backend, server, field string) string {
	t.Helper()
	var col map[string]int
	for _, line := range strings.Split(dump, "\n") {
		if col == nil {
			if !strings.Contains(line, "pxname") || !strings.Contains(line, "svname") {
				continue
			}
			col = map[string]int{}
			for i, name := range strings.Split(strings.TrimPrefix(line, "# "), ",") {
				col[strings.TrimSpace(name)] = i
			}
			if _, ok := col[field]; !ok {
				t.Fatalf("stats header has no %s column", field)
			}
			continue
		}
		v := strings.Split(line, ",")
		if len(v) <= col[field] || v[0] != backend || v[1] != server {
			continue
		}
		return v[col[field]]
	}
	t.Fatalf("no %s/%s column %s in stats", backend, server, field)
	return ""
}
