package rpcserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport/rpcpb/rpcpbconnect"
)

// TestAttachOriginThroughNginxAndVersiond is the old non-HA wrapping:
// nginx location /devshard/ (exempt 500 r/s, burst 2000) sets X-Real-IP from
// $remote_addr and strips /devshard/, then the real versiond Handler keeps
// that header and strips the version. A caller header naming the other
// address must not move the flood onto that address.
func TestAttachOriginThroughNginxAndVersiond(t *testing.T) {
	nginxBin, err := exec.LookPath("nginx")
	require.NoError(t, err, "nginx is required; CI installs it in build-and-test-devshard")

	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 100, Now: clock.Now})

	var mu sync.Mutex
	var seen []string
	child := httptest.NewServer(recordRealIP(&mu, &seen, childRPC(auth)))
	t.Cleanup(child.Close)
	childHost := strings.TrimPrefix(child.URL, "http://")

	hop := buildVersiondHop(t)
	versiondAddr := freeAddr(t)
	startProcess(t, exec.Command(hop), filepath.Join(t.TempDir(), "hop.log"), func(cmd *exec.Cmd) {
		cmd.Env = append(os.Environ(),
			"HOP_LISTEN="+versiondAddr,
			"HOP_CHILD="+childHost,
			"HOP_VERSION=v6",
		)
	}, versiondAddr)

	nginxPort := freePort(t)
	conf := writeNginxConf(t, nginxPort, versiondAddr)
	nginxAddr := fmt.Sprintf("127.0.0.1:%d", nginxPort)
	startProcess(t, exec.Command(nginxBin, "-p", filepath.Dir(conf), "-c", conf), filepath.Join(t.TempDir(), "nginx.log"), nil, nginxAddr)
	waitTCP(t, fmt.Sprintf("[::1]:%d", nginxPort))

	direct := &http.Client{Transport: &http.Transport{Proxy: nil}}
	flood := rpcpbconnect.NewPeerAuthServiceClient(direct, attachBase("127.0.0.1", nginxPort))
	other := rpcpbconnect.NewPeerAuthServiceClient(direct, attachBase("::1", nginxPort))

	for i := 0; i < defaultOriginAttachBurst; i++ {
		err := postSpoofed(t, flood, fmt.Sprintf("wrap-burn-%04d-xxxxx", i), "::1", true)
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "nginx must let the flood through to the child")
	}
	calls := spy.n.Load()
	err = postSpoofed(t, flood, "wrap-burn-over-xxxxx", "::1", true)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Contains(t, err.Error(), attachOriginLimitMessage)
	require.Equal(t, calls, spy.n.Load(), "the origin refusal is before ECDSA")

	err = postSpoofed(t, other, "wrap-other-addr-nonce", "127.0.0.1", false)
	require.NoError(t, err, "the address named in the spoofed header still attaches")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seen, defaultOriginAttachBurst+2)
	for i := 0; i < defaultOriginAttachBurst+1; i++ {
		require.Equal(t, "127.0.0.1", seen[i], "nginx overwrites the caller X-Real-IP")
	}
	require.Equal(t, "::1", seen[len(seen)-1])
}

func attachBase(host string, port int) string {
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%d/devshard/v6/sessions/%s/rpc", host, port, testEscrowID)
}

func postSpoofed(t *testing.T, client rpcpbconnect.PeerAuthServiceClient, nonce, spoof string, bad bool) error {
	t.Helper()
	signer := testutil.MustGenerateKey(t)
	req, err := signedAttach(signer, []byte(nonce), time.Unix(1_700_000_000, 0).Unix())
	require.NoError(t, err)
	if bad {
		req.Signature[0] ^= 0xff
	}
	creq := connect.NewRequest(req)
	creq.Header().Set(originIPHeader, spoof)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = client.Attach(ctx, creq)
	return err
}

func recordRealIP(mu *sync.Mutex, seen *[]string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*seen = append(*seen, r.Header.Get(originIPHeader))
		mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// childRPC is the devshard mount: /sessions/{id}/rpc/{procedure} becomes the
// Connect procedure path the mux serves.
func childRPC(auth *PeerAuthHandler) http.Handler {
	inner := withTestEscrow(NewMux(auth, nil))
	mount := "/sessions/" + testEscrowID + "/rpc"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, mount+"/") {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		u := *r.URL
		u.Path = strings.TrimPrefix(r.URL.Path, mount)
		u.RawPath = ""
		r2 := r.WithContext(r.Context())
		r2.URL = &u
		inner.ServeHTTP(w, r2)
	})
}

func buildVersiondHop(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "versiond-hop")
	cmd := exec.Command("go", "build", "-o", bin, "./internal/proxy/testhop")
	cmd.Dir = versionedModule(t)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "build versiond hop:\n%s", out)
	return bin
}

func versionedModule(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dir := filepath.Dir(file)
	for i := 0; i < 6; i++ {
		cand := filepath.Join(dir, "versioned", "go.mod")
		if _, err := os.Stat(cand); err == nil {
			return filepath.Join(dir, "versioned")
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("versioned module not found")
	return ""
}

func writeNginxConf(t *testing.T, port int, versiondAddr string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"client_body", "proxy", "fastcgi", "uwsgi", "scgi"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, name), 0o755))
	}
	conf := filepath.Join(dir, "nginx.conf")
	body := fmt.Sprintf(`worker_processes 1;
error_log %s/error.log info;
pid %s/nginx.pid;
daemon off;
events { worker_connections 64; }
http {
  access_log %s/access.log;
  client_body_temp_path %s/client_body;
  proxy_temp_path %s/proxy;
  fastcgi_temp_path %s/fastcgi;
  uwsgi_temp_path %s/uwsgi;
  scgi_temp_path %s/scgi;
  default_type application/octet-stream;
  limit_req_zone $binary_remote_addr zone=exempt_zone:1m rate=500r/s;
  server {
    listen 127.0.0.1:%d;
    listen [::1]:%d;
    location /devshard/ {
      limit_req zone=exempt_zone burst=2000 nodelay;
      proxy_pass http://%s/;
      proxy_http_version 1.1;
      proxy_set_header Host $host;
      proxy_set_header X-Real-IP $remote_addr;
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
      proxy_set_header Connection "";
    }
  }
}
`, dir, dir, dir, dir, dir, dir, dir, dir, port, port, versiondAddr)
	require.NoError(t, os.WriteFile(conf, []byte(body), 0o644))
	return conf
}

// startProcess runs cmd and waits until addr accepts TCP. prepare runs
// before the process starts.
func startProcess(t *testing.T, cmd *exec.Cmd, logPath string, prepare func(*exec.Cmd), addr string) {
	t.Helper()
	if prepare != nil {
		prepare(cmd)
	}
	logf, err := os.Create(logPath)
	require.NoError(t, err)
	cmd.Stdout = logf
	cmd.Stderr = logf
	require.NoError(t, cmd.Start())
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		_ = logf.Close()
	})
	waitReady(t, cmd, exited, addr, logPath)
}

func waitReady(t *testing.T, cmd *exec.Cmd, exited <-chan struct{}, addr, logPath string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			t.Fatalf("%s exited before listen:\n%s", cmd.Path, readLog(logPath))
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		last = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("listen %s: %v\n%s", addr, last, readLog(logPath))
}

func waitTCP(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		last = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("listen %s: %v", addr, last)
}

func readLog(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	return string(b)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func freeAddr(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("127.0.0.1:%d", freePort(t))
}
