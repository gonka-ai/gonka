package user

import (
	"path/filepath"
	"runtime"
	"testing"
	"weak"

	"github.com/stretchr/testify/require"

	"devshard/transport"
)

// Test flow:
//  1. Open two sessions on the same host so they share one peer connection, as escrows of one host group do.
//  2. Take admission-stripped clients from the first session the way catch-up and finalize do, release them, and close the session.
//  3. Assert the closed session and its host client are collected while the other session keeps the shared connection alive.
func TestSession_AClosedSessionIsCollectedWhileItsPeerConnLivesOn(t *testing.T) {
	const privateKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	host := "host-rpc-collect"
	hostBridge := rpcLifecycleBridge{slots: []string{host}}
	open := func(escrowID string) *Session {
		session, _, err := NewHTTPSession(HTTPSessionConfig{
			PrivateKeyHex:     privateKeyHex,
			EscrowID:          escrowID,
			Bridge:            hostBridge,
			StoragePath:       filepath.Join(t.TempDir(), escrowID),
			RoutePrefix:       "/devshard/dev",
			ExtraClientConfig: rpcSignaturesConfig(),
		})
		require.NoError(t, err)
		return session
	}
	survivor := open("9802")
	t.Cleanup(func() { require.NoError(t, survivor.Close()) })

	closedSession, closedClient := func() (weak.Pointer[Session], weak.Pointer[transport.RPCClient]) {
		session := open("9801")
		client, ok := session.clients[0].(*transport.RPCClient)
		require.True(t, ok, "host client is %T", session.clients[0])
		_, release := session.getFinalizeClients()
		release()
		_, releaseOne := session.finalizeClient(0)
		releaseOne()
		sessionPointer, clientPointer := weak.Make(session), weak.Make(client)
		require.NoError(t, session.Close())
		return sessionPointer, clientPointer
	}()
	for range 3 {
		runtime.GC()
	}

	require.True(t, transport.PeerConnRegistered(host, "dev"), "the surviving session keeps the shared connection")
	require.Nil(t, closedSession.Value(), "the closed session is still reachable")
	require.Nil(t, closedClient.Value(), "the closed session's host client is still reachable")
}
