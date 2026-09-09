package scenarios

import (
	"os"
	"testing"

	"common/httpguard"
)

// TestMain enables the permissive dialer for this package's tests. Height-sync
// E2E-in-unit tests POST to httptest servers on 127.0.0.1 through the real
// transport, which carries the dial-time SSRF guard (secure by default) and
// would otherwise reject every loopback connection.
func TestMain(m *testing.M) {
	httpguard.SetAllowPrivate(true)
	os.Exit(m.Run())
}
