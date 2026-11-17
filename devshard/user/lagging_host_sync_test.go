package user

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Compose more diffs than the lag threshold without sending any of them, so every host falls behind.
//  2. Wait for the session's own background sync.
//  3. Assert every host's cursor reaches the session's nonce, so a host that received no request still holds the escrow's state.
func TestAHostFarBehindIsSyncedWithoutARequest(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	t.Cleanup(func() { _ = session.Close() })

	for range laggingHostSyncNonces + 44 {
		session.mu.Lock()
		_, _, err := session.composeDiffLocked(context.Background(), nil)
		session.mu.Unlock()
		require.NoError(t, err)
	}

	require.Eventually(t, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		for hostIdx := range session.clients {
			if session.nonce-session.hostSyncNonce[hostIdx] >= laggingHostSyncNonces {
				return false
			}
		}
		return true
	}, 10*time.Second, 10*time.Millisecond)
}
