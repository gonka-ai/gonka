package harness

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

// pickFreePort returns an ephemeral localhost TCP port for citest host publishing.
func pickFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
