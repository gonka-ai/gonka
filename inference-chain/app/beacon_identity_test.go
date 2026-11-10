package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cosmossdk.io/log"
	"github.com/stretchr/testify/require"
)

const testValAddrHex = "0123456789ABCDEF0123456789ABCDEF01234567"

// statusBody is a trimmed CometBFT 0.38 /status reply.
func statusBody(addr string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":-1,"result":{"node_info":{"network":"gonka-test"},`+
		`"sync_info":{"latest_block_height":"10"},"validator_info":{"address":%q,`+
		`"pub_key":{"type":"tendermint/PubKeyEd25519","value":"AAAA"},"voting_power":"10"}}}`, addr)
}

func TestParseStatusAddress(t *testing.T) {
	addr, err := parseStatusAddress([]byte(statusBody(testValAddrHex)))
	require.NoError(t, err)
	require.Equal(t, strings.ToUpper(testValAddrHex), strings.ToUpper(fmt.Sprintf("%X", addr)))

	for name, body := range map[string]string{
		"not json":      `<html>`,
		"no address":    `{"result":{"validator_info":{}}}`,
		"not hex":       statusBody("zz"),
		"wrong length":  statusBody("0123"),
		"error payload": `{"jsonrpc":"2.0","error":{"code":-32603,"message":"x"}}`,
	} {
		_, err := parseStatusAddress([]byte(body))
		require.Error(t, err, name)
	}
}

func TestStatusEndpoint(t *testing.T) {
	for laddr, want := range map[string]string{
		"tcp://0.0.0.0:26657":   "http://127.0.0.1:26657/status",
		"tcp://127.0.0.1:26657": "http://127.0.0.1:26657/status",
		"tcp://[::]:26657":      "http://127.0.0.1:26657/status",
		"tcp://10.0.0.5:3000":   "http://10.0.0.5:3000/status",
		"http://localhost:1234": "http://localhost:1234/status",
		"unix:///tmp/rpc.sock":  "http://unix/status",
	} {
		got, client, err := statusEndpoint(laddr)
		require.NoError(t, err, laddr)
		require.Equal(t, want, got, laddr)
		require.NotNil(t, client)
	}
	for _, bad := range []string{"tcp://nohost", "grpc://1.2.3.4:5", "unix://", "::bad"} {
		_, _, err := statusEndpoint(bad)
		require.Error(t, err, bad)
	}

	id, err := newCometSelfIdentity("", log.NewNopLogger())
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:26657/status", id.statusURL, "CometBFT default")
}

func TestCometSelfIdentityFetchesOverTCP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/status", r.URL.Path)
		_, _ = w.Write([]byte(statusBody(testValAddrHex)))
	}))
	defer srv.Close()

	id, err := newCometSelfIdentity("tcp://"+srv.Listener.Addr().String(), log.NewNopLogger())
	require.NoError(t, err)
	_, ok := id.ConsensusAddress()
	require.False(t, ok, "unknown before start")

	id.start(context.Background())
	defer id.close()
	require.Eventually(t, func() bool { _, ok := id.ConsensusAddress(); return ok }, 5*time.Second, 10*time.Millisecond)
	addr, _ := id.ConsensusAddress()
	require.Equal(t, strings.ToUpper(testValAddrHex), fmt.Sprintf("%X", addr))
}

func TestCometSelfIdentityFetchesOverUnixSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "rpc") // short path: unix socket names are length-limited
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "rpc.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(statusBody(testValAddrHex)))
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	id, err := newCometSelfIdentity("unix://"+sock, log.NewNopLogger())
	require.NoError(t, err)
	addr, err := id.fetch(context.Background())
	require.NoError(t, err)
	require.Len(t, addr, 20)
}

// The RPC comes up after the app: the lookup keeps retrying and then succeeds.
func TestCometSelfIdentityRetriesUntilRPCIsUp(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 2 {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(statusBody(testValAddrHex)))
	}))
	defer srv.Close()

	id, err := newCometSelfIdentity("tcp://"+srv.Listener.Addr().String(), log.NewNopLogger())
	require.NoError(t, err)
	id.start(context.Background())
	defer id.close()
	require.Eventually(t, func() bool { _, ok := id.ConsensusAddress(); return ok }, 10*time.Second, 20*time.Millisecond)
	require.GreaterOrEqual(t, calls.Load(), int32(2))
}

func TestCometSelfIdentityCloseStopsRetrying(t *testing.T) {
	id, err := newCometSelfIdentity("tcp://127.0.0.1:1", log.NewNopLogger()) // nothing listens
	require.NoError(t, err)
	id.start(context.Background())
	done := make(chan struct{})
	go func() { id.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("close did not stop the lookup")
	}
	_, ok := id.ConsensusAddress()
	require.False(t, ok)
	id.close() // idempotent enough: second close returns (done already closed)
}

// SetBeaconSelfForTest sets this node's consensus address as if read from
// /status (for app_test, which has no CometBFT node).
func (app *App) SetBeaconSelfForTest(addr []byte) {
	app.beaconIdentity.mu.Lock()
	defer app.beaconIdentity.mu.Unlock()
	app.beaconIdentity.addr = addr
}
