package harness

import (
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

var pickedPorts = struct {
	sync.Mutex
	ports map[int]struct{}
}{ports: make(map[int]struct{})}

// pickFreePort returns an ephemeral TCP port for generated service listen ports.
// Host publishing is randomized later by Docker Compose.
func pickFreePort(t *testing.T) int {
	t.Helper()
	for range 100 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		port := ln.Addr().(*net.TCPAddr).Port
		require.NoError(t, ln.Close())

		pickedPorts.Lock()
		_, used := pickedPorts.ports[port]
		if !used {
			pickedPorts.ports[port] = struct{}{}
		}
		pickedPorts.Unlock()
		if !used {
			return port
		}
	}
	t.Fatal("could not pick a unique localhost port")
	return 0
}
