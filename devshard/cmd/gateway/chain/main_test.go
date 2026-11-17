package chain

import (
	"os"
	"testing"

	"common/httpguard"
)

// TestMain opens the dial guard for this package's loopback httptest servers.
func TestMain(m *testing.M) {
	httpguard.SetAllowPrivate(true)
	os.Exit(m.Run())
}
