package nonces

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"devshard/cmd/gateway/config"
)

// Test flow:
//  1. Open an enabled ledger and take the accounting listener it builds.
//  2. Assert the listener keeps its bind on every interface and sets a read, a write and an idle timeout, so a stalled reply fails and a kept-alive connection is reaped.
//  3. Serve an accounting request through the listener's handler and assert it is answered.
func TestTheAccountingListenerBoundsEveryReadWriteAndIdleConnection(t *testing.T) {
	ledger := Open(config.NonceAccounting{Enabled: true, Port: 9191, SnapshotSeconds: 300}, t.TempDir(), nil, func() time.Time { return time.Unix(0, 0).UTC() })
	if ledger == nil || ledger.listener == nil {
		t.Fatal("Open() built no accounting listener for an enabled ledger")
	}
	t.Cleanup(func() { _ = ledger.Close() })
	listener := ledger.listener

	if !strings.HasPrefix(listener.Addr, ":") {
		t.Errorf("listener address = %q, want one with no host, which binds every interface", listener.Addr)
	}
	if listener.ReadTimeout <= 0 || listener.WriteTimeout <= 0 || listener.IdleTimeout <= 0 {
		t.Errorf("listener read/write/idle timeouts = %v/%v/%v, want each above 0", listener.ReadTimeout, listener.WriteTimeout, listener.IdleTimeout)
	}

	recorder := httptest.NewRecorder()
	listener.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/escrows", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", recorder.Code, recorder.Body.String())
	}
}
