package scenarios

import (
	"os"
	"testing"

	"common/httpguard"

	"devshard/state"
)

// TestMain enables the permissive dialer for this package's tests. Height-sync
// E2E-in-unit tests POST to httptest servers on 127.0.0.1 through the real
// transport, which carries the dial-time SSRF guard (secure by default) and
// would otherwise reject every loopback connection.
//
// The fixture pin is hsE2EProtocolVersion. Host state machines pick it up
// when the caller does not pass a version. The user session does pass one,
// from hsE2ERoutePrefix, so the two stay on the same tag.
func TestMain(m *testing.M) {
	httpguard.SetAllowPrivate(true)
	state.SetFixtureProtocolVersion(hsE2EProtocolVersion)
	os.Exit(m.Run())
}
