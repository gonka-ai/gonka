package user

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"devshard/bridge"
	"devshard/host"
	"devshard/storage"
	"devshard/transport"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

func TestNewHTTPSessionRequiresRoutePrefix(t *testing.T) {
	_, _, err := NewHTTPSession(HTTPSessionConfig{})
	require.ErrorContains(t, err, "RoutePrefix is required")
	require.ErrorContains(t, err, "/devshard/{version}")
	require.False(t, errors.Is(err, ErrLocalStateUnrecoverable))
}

func TestNewHTTPSessionOpenStorageFailureStaysFatal(t *testing.T) {
	const privateKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	storagePath := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(storagePath, []byte("occupied"), 0o600))

	_, _, err := NewHTTPSession(HTTPSessionConfig{
		PrivateKeyHex: privateKeyHex,
		EscrowID:      "9801",
		Bridge:        httpsessionTestBridge{},
		StoragePath:   storagePath,
		RoutePrefix:   "/devshard/dev",
	})

	// Open errors are environmental (permissions, disk), not proven history
	// corruption: they must not carry the deactivate-and-skip sentinel.
	require.Error(t, err)
	require.ErrorContains(t, err, "open storage")
	require.False(t, errors.Is(err, ErrLocalStateUnrecoverable))
}

func TestNewHTTPSessionClassifiesNonSequentialReplay(t *testing.T) {
	const privateKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	storagePath := filepath.Join(t.TempDir(), "session")
	store, err := storage.NewSQLite(storagePath)
	require.NoError(t, err)
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "9801",
		EpochID:        7,
		Version:        "dev",
		CreatorAddr:    "creator",
		Group:          []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "host-1"}},
		InitialBalance: 1_000_000,
	}))
	require.NoError(t, store.AppendDiff("9801", types.DiffRecord{Diff: types.Diff{Nonce: 1}}))
	require.NoError(t, store.AppendDiff("9801", types.DiffRecord{Diff: types.Diff{Nonce: 3}}))
	require.NoError(t, store.Close())

	_, _, err = NewHTTPSession(HTTPSessionConfig{
		PrivateKeyHex: privateKeyHex,
		EscrowID:      "9801",
		Bridge:        httpsessionTestBridge{},
		StoragePath:   storagePath,
		RoutePrefix:   "/devshard/dev",
	})

	require.ErrorIs(t, err, ErrLocalStateUnrecoverable)
	require.ErrorContains(t, err, "recover session")
	require.ErrorContains(t, err, "missing nonce 2, next stored nonce is 3")
}

func TestDeferredWarmKeyResolverAvoidsExternalCallsDuringRecovery(t *testing.T) {
	calls := 0
	resolver, enable := deferredWarmKeyResolver(func(_, _ string) (bool, error) {
		calls++
		return true, nil
	})

	ok, err := resolver("warm", "cold")
	require.NoError(t, err)
	require.False(t, ok)
	require.Zero(t, calls)

	enable()
	ok, err = resolver("warm", "cold")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1, calls)
}

func TestNewHTTPSessionUsesRouteVersionForStorageBind(t *testing.T) {
	const privateKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	storagePath := t.TempDir() + "/session.db"

	session, _, err := NewHTTPSession(HTTPSessionConfig{
		PrivateKeyHex: privateKeyHex,
		EscrowID:      "9801",
		Bridge:        httpsessionTestBridge{},
		StoragePath:   storagePath,
		RoutePrefix:   " /devshard/dev/ ",
	})
	require.NoError(t, err)
	require.NoError(t, session.Close())

	store, err := storage.NewSQLite(storagePath)
	require.NoError(t, err)
	defer store.Close()

	meta, err := store.GetSessionMeta("9801")
	require.NoError(t, err)
	require.Equal(t, "dev", meta.Version)
}

type httpsessionTestBridge struct{}

func (httpsessionTestBridge) OnEscrowCreated(bridge.EscrowInfo) error { return nil }
func (httpsessionTestBridge) OnSettlementProposed(string, []byte, uint64) error {
	return nil
}
func (httpsessionTestBridge) OnSettlementFinalized(string) error { return nil }

func (httpsessionTestBridge) GetEscrow(string) (*bridge.EscrowInfo, error) {
	return &bridge.EscrowInfo{
		EscrowID:       "9801",
		Amount:         1_000_000,
		CreatorAddress: "creator",
		Slots:          []string{"host-1"},
		TokenPrice:     1,
		EpochID:        7,
	}, nil
}

func (httpsessionTestBridge) GetHostInfo(address string) (*bridge.HostInfo, error) {
	return &bridge.HostInfo{Address: address, URL: "http://host.test"}, nil
}

func (httpsessionTestBridge) GetValidationThreshold(uint64, string) (*bridge.Decimal, error) {
	return nil, nil
}

func (httpsessionTestBridge) VerifyWarmKey(string, string) (bool, error) { return true, nil }
func (httpsessionTestBridge) SubmitDisputeState(string, []byte, uint64, map[uint32][]byte) error {
	return nil
}

type rpcLifecycleBridge struct {
	httpsessionTestBridge
	slots      []string
	failHost   string
	absentHost string
}

func (b rpcLifecycleBridge) GetEscrow(id string) (*bridge.EscrowInfo, error) {
	info, err := b.httpsessionTestBridge.GetEscrow(id)
	if err != nil {
		return nil, err
	}
	info.EscrowID = id
	if len(b.slots) > 0 {
		info.Slots = append([]string(nil), b.slots...)
	}
	return info, nil
}

func (b rpcLifecycleBridge) GetHostInfo(address string) (*bridge.HostInfo, error) {
	if b.failHost != "" && address == b.failHost {
		return nil, errors.New("host info failed")
	}
	if b.absentHost != "" && address == b.absentHost {
		return nil, fmt.Errorf("participant %s: %w", address, bridge.ErrParticipantNotFound)
	}
	return &bridge.HostInfo{Address: address, URL: "http://127.0.0.1:1"}, nil
}

func rpcSignaturesConfig() *transport.ClientConfig {
	return &transport.ClientConfig{RPCEndpoints: transport.ParseRPCEndpoints(transport.EndpointSignatures)}
}

// hungHostBridge blocks GetHostInfo for one host until gate is closed, and
// can publish a blank URL for another.
type hungHostBridge struct {
	rpcLifecycleBridge
	hungHost  string
	gate      chan struct{}
	blankHost string
}

func (b hungHostBridge) GetHostInfo(address string) (*bridge.HostInfo, error) {
	if address == b.hungHost {
		<-b.gate
	}
	if address == b.blankHost {
		return &bridge.HostInfo{Address: address, URL: "  "}, nil
	}
	return b.rpcLifecycleBridge.GetHostInfo(address)
}

func shortHostLookupOpenWait(t *testing.T) {
	t.Helper()
	prev := hostLookupOpenWait
	hostLookupOpenWait = 50 * time.Millisecond
	t.Cleanup(func() { hostLookupOpenWait = prev })
}

// Tests that one host's chain error does not stop the escrow from opening.
//  1. Fail GetHostInfo for one of two hosts and open the session.
//  2. Send to each host.
//  3. Expect the healthy host to keep its real client, the failed one to refuse with ErrHostUnresolved and report itself spent, and Close to release the PeerConn.
func TestNewHTTPSession_GetHostInfoErrorOpensWithLazyHost(t *testing.T) {
	const privateKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	hostA := "host-rpc-a"
	hostB := "host-rpc-b"
	session, _, err := NewHTTPSession(HTTPSessionConfig{
		PrivateKeyHex: privateKeyHex,
		EscrowID:      "9801",
		Bridge: rpcLifecycleBridge{
			slots:    []string{hostA, hostB},
			failHost: hostB,
		},
		StoragePath:       filepath.Join(t.TempDir(), "session"),
		RoutePrefix:       "/devshard/dev",
		ExtraClientConfig: rpcSignaturesConfig(),
	})
	require.NoError(t, err, "one host's URL must not gate the whole escrow")
	clients := session.Clients()
	require.Len(t, clients, 2)
	require.IsType(t, &transport.RPCClient{}, clients[0])
	lazy, ok := clients[1].(*lazyHostClient)
	require.True(t, ok, "unresolved host is %T", clients[1])

	_, err = lazy.Send(context.Background(), host.HostRequest{}, nil, nil)
	require.ErrorIs(t, err, ErrHostUnresolved)
	require.ErrorContains(t, err, "host info failed")
	require.GreaterOrEqual(t, lazy.ChatBudgetWait(), unresolvedHostChatWait)

	require.True(t, transport.PeerConnRegistered(hostA, "dev"))
	require.NoError(t, session.Close())
	require.False(t, transport.PeerConnRegistered(hostA, "dev"))
}

// Test flow:
//  1. Open a session that keeps absent hosts as dead slots, whose group has one host the chain no longer lists.
//  2. Require the session to open, and that host's slot to fail every call as absent from the chain.
func TestNewHTTPSession_HostAbsentFromChainLeavesADeadSlot(t *testing.T) {
	const privateKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	session, _, err := NewHTTPSession(HTTPSessionConfig{
		OpenAbsentHostsAsDeadSlots: true,
		PrivateKeyHex:              privateKeyHex,
		EscrowID:                   "9801",
		Bridge:                     rpcLifecycleBridge{slots: []string{"host-live", "host-gone"}, absentHost: "host-gone"},
		StoragePath:                filepath.Join(t.TempDir(), "session"),
		RoutePrefix:                "/devshard/dev",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	_, err = session.clients[1].Send(context.Background(), host.HostRequest{}, nil, nil)

	require.ErrorIs(t, err, ErrHostAbsentFromChain)
}

// Tests that a slot the chain has no participant for opens like any other unresolved host.
//  1. Answer GetHostInfo for one of two hosts with ErrParticipantNotFound and open the session.
//  2. Send to the absent host.
//  3. Expect the session open, the absent host lazy and reported spent, and its calls refused with ErrHostUnresolved wrapping that sentinel.
func TestNewHTTPSession_ParticipantNotFoundOpensWithLazyHost(t *testing.T) {
	const privateKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	hostA := "host-rpc-missing-a"
	hostB := "host-rpc-missing-b"
	session, _, err := NewHTTPSession(HTTPSessionConfig{
		PrivateKeyHex: privateKeyHex,
		EscrowID:      "9801",
		Bridge: missingParticipantBridge{
			rpcLifecycleBridge: rpcLifecycleBridge{slots: []string{hostA, hostB}},
			missing:            hostB,
		},
		StoragePath:       filepath.Join(t.TempDir(), "session"),
		RoutePrefix:       "/devshard/dev",
		ExtraClientConfig: rpcSignaturesConfig(),
	})
	require.NoError(t, err, "one participant absent from the chain must not gate the whole escrow")
	t.Cleanup(func() { _ = session.Close() })
	lazy, ok := session.Clients()[1].(*lazyHostClient)
	require.True(t, ok, "absent host is %T", session.Clients()[1])

	_, err = lazy.Send(context.Background(), host.HostRequest{}, nil, nil)

	require.ErrorIs(t, err, ErrHostUnresolved)
	require.ErrorIs(t, err, bridge.ErrParticipantNotFound)
	require.GreaterOrEqual(t, lazy.ChatBudgetWait(), unresolvedHostChatWait)
}

type missingParticipantBridge struct {
	rpcLifecycleBridge
	missing string
}

func (b missingParticipantBridge) GetHostInfo(address string) (*bridge.HostInfo, error) {
	if address == b.missing {
		return nil, fmt.Errorf("Participant %s: %w", address, bridge.ErrParticipantNotFound)
	}
	return b.rpcLifecycleBridge.GetHostInfo(address)
}

// Tests that a hung host lookup holds the open no longer than hostLookupOpenWait and fills in afterwards.
//  1. Hang GetHostInfo for one host and open the session under a short open wait.
//  2. Expect the open to return with that host lazy and no PeerConn for it.
//  3. Release the chain and expect the host to build, register its PeerConn, and release it on Close.
func TestNewHTTPSession_HungHostInfoDoesNotBlockOpen(t *testing.T) {
	shortHostLookupOpenWait(t)
	const privateKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	hostA := "host-rpc-hung-a"
	hostB := "host-rpc-hung-b"
	gate := make(chan struct{})
	released := false
	release := func() {
		if !released {
			released = true
			close(gate)
		}
	}
	t.Cleanup(release)

	opened := time.Now()
	session, _, err := NewHTTPSession(HTTPSessionConfig{
		PrivateKeyHex: privateKeyHex,
		EscrowID:      "9801",
		Bridge: hungHostBridge{
			rpcLifecycleBridge: rpcLifecycleBridge{slots: []string{hostA, hostB}},
			hungHost:           hostB,
			gate:               gate,
		},
		StoragePath:       filepath.Join(t.TempDir(), "session"),
		RoutePrefix:       "/devshard/dev",
		ExtraClientConfig: rpcSignaturesConfig(),
	})
	require.NoError(t, err)
	require.Less(t, time.Since(opened), 5*time.Second)
	lazy, ok := session.Clients()[1].(*lazyHostClient)
	require.True(t, ok)
	require.Empty(t, lazy.BaseURL())
	require.False(t, transport.PeerConnRegistered(hostB, "dev"))

	release()
	require.Eventually(t, func() bool { return lazy.BaseURL() != "" }, 5*time.Second, time.Millisecond)
	require.True(t, transport.PeerConnRegistered(hostB, "dev"))
	require.Less(t, lazy.ChatBudgetWait(), unresolvedHostChatWait, "a built host is no longer reported spent")

	require.NoError(t, session.Close())
	require.False(t, transport.PeerConnRegistered(hostB, "dev"))
}

// Tests that a host publishing a blank URL opens lazy instead of getting a client that dials nothing.
//  1. Publish "  " as one host's InferenceUrl and open the session.
//  2. Send to that host.
//  3. Expect ErrHostUnresolved wrapping transport.ErrPeerURLMissing.
func TestNewHTTPSession_BlankHostURLOpensLazy(t *testing.T) {
	const privateKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	hostA := "host-rpc-blank-a"
	hostB := "host-rpc-blank-b"
	session, _, err := NewHTTPSession(HTTPSessionConfig{
		PrivateKeyHex: privateKeyHex,
		EscrowID:      "9801",
		Bridge: hungHostBridge{
			rpcLifecycleBridge: rpcLifecycleBridge{slots: []string{hostA, hostB}},
			blankHost:          hostB,
		},
		StoragePath:       filepath.Join(t.TempDir(), "session"),
		RoutePrefix:       "/devshard/dev",
		ExtraClientConfig: rpcSignaturesConfig(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	lazy, ok := session.Clients()[1].(*lazyHostClient)
	require.True(t, ok)
	_, err = lazy.Send(context.Background(), host.HostRequest{}, nil, nil)
	require.ErrorIs(t, err, ErrHostUnresolved)
	require.ErrorIs(t, err, transport.ErrPeerURLMissing)
}

func TestNewHTTPSession_RecoverErrorReleasesPeerConn(t *testing.T) {
	const privateKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	host := "host-rpc-recover"
	storagePath := filepath.Join(t.TempDir(), "session")
	store, err := storage.NewSQLite(storagePath)
	require.NoError(t, err)
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "9801",
		EpochID:        7,
		Version:        "dev",
		CreatorAddr:    "creator",
		Group:          []types.SlotAssignment{{SlotID: 0, ValidatorAddress: host}},
		InitialBalance: 1_000_000,
	}))
	require.NoError(t, store.AppendDiff("9801", types.DiffRecord{Diff: types.Diff{Nonce: 1}}))
	require.NoError(t, store.AppendDiff("9801", types.DiffRecord{Diff: types.Diff{Nonce: 3}}))
	require.NoError(t, store.Close())

	_, _, err = NewHTTPSession(HTTPSessionConfig{
		PrivateKeyHex:     privateKeyHex,
		EscrowID:          "9801",
		Bridge:            rpcLifecycleBridge{slots: []string{host}},
		StoragePath:       storagePath,
		RoutePrefix:       "/devshard/dev",
		ExtraClientConfig: rpcSignaturesConfig(),
	})
	require.ErrorIs(t, err, ErrLocalStateUnrecoverable)
	require.False(t, transport.PeerConnRegistered(host, "dev"))
}

func TestSession_Close_SharedPeerConnStaysUntilLast(t *testing.T) {
	const privateKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"
	host := "host-rpc-share"
	br := rpcLifecycleBridge{slots: []string{host}}
	s1, _, err := NewHTTPSession(HTTPSessionConfig{
		PrivateKeyHex:     privateKeyHex,
		EscrowID:          "9801",
		Bridge:            br,
		StoragePath:       filepath.Join(t.TempDir(), "s1"),
		RoutePrefix:       "/devshard/dev",
		ExtraClientConfig: rpcSignaturesConfig(),
	})
	require.NoError(t, err)
	s2, _, err := NewHTTPSession(HTTPSessionConfig{
		PrivateKeyHex:     privateKeyHex,
		EscrowID:          "9802",
		Bridge:            br,
		StoragePath:       filepath.Join(t.TempDir(), "s2"),
		RoutePrefix:       "/devshard/dev",
		ExtraClientConfig: rpcSignaturesConfig(),
	})
	require.NoError(t, err)
	require.True(t, transport.PeerConnRegistered(host, "dev"))
	require.NoError(t, s1.Close())
	require.True(t, transport.PeerConnRegistered(host, "dev"), "first Close must keep the shared PeerConn")
	require.NoError(t, s2.Close())
	require.False(t, transport.PeerConnRegistered(host, "dev"))
}
