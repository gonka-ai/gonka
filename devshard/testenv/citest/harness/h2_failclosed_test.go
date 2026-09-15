package harness

import (
	"bufio"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

// TestHTTP2CleartextFailsClosedWithoutH2C is the 0.2.15-v5 versiond listen:
// `&http.Server{}` without h2c.NewHandler. An HTTP/2-only client must error.
// A default http.Client would silently speak HTTP/1.1 — that is not a
// multiplexed /rpc/ session and must not be treated as h2 success.
func TestHTTP2CleartextFailsClosedWithoutH2C(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/rpc/")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 1, resp.ProtoMajor, "default client is HTTP/1.1 fan-out, not h2")

	tr := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/rpc/", nil)
	require.NoError(t, err)
	h2resp, err := client.Do(req)
	if err == nil {
		defer h2resp.Body.Close()
		t.Fatalf("h2c client succeeded Proto=%s status=%d; h2 to &http.Server{} must fail closed", h2resp.Proto, h2resp.StatusCode)
	}
}

func TestBaselinePinVersiondHasNoH2C(t *testing.T) {
	pinPath := filepath.Join("..", "..", "baselines", "0.2.15-v5.txt")
	sha := baselinePinSHA(t, pinPath)
	require.NotEmpty(t, sha)

	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	require.NoError(t, err)

	mainGo := gitShow(t, repoRoot, sha, "versioned/cmd/versiond/main.go")
	require.Contains(t, mainGo, "srv := &http.Server{")
	require.NotContains(t, mainGo, "h2c")

	proxyGo := gitShow(t, repoRoot, sha, "versioned/internal/proxy/proxy.go")
	require.Contains(t, proxyGo, "httputil.ReverseProxy")
	require.NotContains(t, proxyGo, "http2.Transport")
	require.NotContains(t, proxyGo, "ForceAttemptHTTP2")
	require.NotContains(t, proxyGo, "h2c")
}

func baselinePinSHA(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "sha:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "sha:"))
		}
	}
	require.NoError(t, sc.Err())
	t.Fatalf("no sha: in %s", path)
	return ""
}

func gitShow(t *testing.T, repoRoot, sha, path string) string {
	t.Helper()
	cmd := exec.Command("git", "show", sha+":"+path)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("git show %s:%s: %v\n%s", sha, path, err, out)
	}
	return string(out)
}
