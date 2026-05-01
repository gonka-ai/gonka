package bridge_test

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/stretchr/testify/require"

	devbridge "devshard/bridge"
	testenvbridge "devshard/testenv/bridge"
	"devshard/testenv/proto/mockchainpb"
)

const bufSize = 1 << 16

// fakeMockChain is a minimal in-memory MockChainServer used to exercise
// the testenv bridge adapter without pulling in the full cmd/mockchain
// package (which is a main package). Every knob the tests need to
// manipulate is exposed as a field or counter on this struct.
type fakeMockChain struct {
	mockchainpb.UnimplementedMockChainServer

	escrow      *mockchainpb.GetDevshardEscrowResponse
	escrowError error

	participant      *mockchainpb.GetParticipantResponse
	participantError error

	grantees      *mockchainpb.GetGranteesResponse
	granteesError error

	escrowCalls      int32
	participantCalls int32
	granteesCalls    int32
}

func (f *fakeMockChain) GetDevshardEscrow(_ context.Context, _ *mockchainpb.GetDevshardEscrowRequest) (*mockchainpb.GetDevshardEscrowResponse, error) {
	atomic.AddInt32(&f.escrowCalls, 1)
	if f.escrowError != nil {
		return nil, f.escrowError
	}
	return f.escrow, nil
}

func (f *fakeMockChain) GetParticipant(_ context.Context, _ *mockchainpb.GetParticipantRequest) (*mockchainpb.GetParticipantResponse, error) {
	atomic.AddInt32(&f.participantCalls, 1)
	if f.participantError != nil {
		return nil, f.participantError
	}
	return f.participant, nil
}

func (f *fakeMockChain) GetGrantees(_ context.Context, _ *mockchainpb.GetGranteesRequest) (*mockchainpb.GetGranteesResponse, error) {
	atomic.AddInt32(&f.granteesCalls, 1)
	if f.granteesError != nil {
		return nil, f.granteesError
	}
	return f.grantees, nil
}

// newTestBridge wires a fakeMockChain behind a bufconn-backed gRPC server
// and returns a bridge plus the raw fake (for assertions) and a cleanup.
func newTestBridge(t *testing.T, fake *fakeMockChain) (*testenvbridge.GRPCBridge, func()) {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	gs := grpc.NewServer()
	mockchainpb.RegisterMockChainServer(gs, fake)

	done := make(chan struct{})
	go func() {
		_ = gs.Serve(lis)
		close(done)
	}()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	client := mockchainpb.NewMockChainClient(conn)
	b := testenvbridge.NewGRPCBridgeWithClient(client)

	cleanup := func() {
		_ = conn.Close()
		gs.GracefulStop()
		<-done
	}
	return b, cleanup
}

// ── GetEscrow ────────────────────────────────────────────────────────────────

func TestGRPCBridge_GetEscrow_MapsFields(t *testing.T) {
	fake := &fakeMockChain{
		escrow: &mockchainpb.GetDevshardEscrowResponse{
			EscrowId:       "1",
			Amount:         1_000_000,
			CreatorAddress: "gonka1creator",
			AppHash:        []byte{0x01, 0x02, 0x03},
			Slots:          []string{"gonka1a", "gonka1b"},
			TokenPrice:     7,
		},
	}
	b, cleanup := newTestBridge(t, fake)
	defer cleanup()

	got, err := b.GetEscrow("1")
	require.NoError(t, err)
	require.Equal(t, "1", got.EscrowID)
	require.Equal(t, uint64(1_000_000), got.Amount)
	require.Equal(t, "gonka1creator", got.CreatorAddress)
	require.Equal(t, []byte{0x01, 0x02, 0x03}, got.AppHash)
	require.Equal(t, []string{"gonka1a", "gonka1b"}, got.Slots)
	require.Equal(t, uint64(7), got.TokenPrice)

	// The bridge copies byte slices so callers can mutate without
	// corrupting the underlying proto response.
	got.AppHash[0] = 0xFF
	require.Equal(t, byte(0x01), fake.escrow.AppHash[0])
}

func TestGRPCBridge_GetEscrow_NotFoundReturnsSentinel(t *testing.T) {
	fake := &fakeMockChain{escrowError: status.Error(codes.NotFound, "nope")}
	b, cleanup := newTestBridge(t, fake)
	defer cleanup()

	_, err := b.GetEscrow("missing")
	require.ErrorIs(t, err, devbridge.ErrEscrowNotFound)
}

func TestGRPCBridge_GetEscrow_OtherErrorWrapped(t *testing.T) {
	fake := &fakeMockChain{escrowError: status.Error(codes.Internal, "kaboom")}
	b, cleanup := newTestBridge(t, fake)
	defer cleanup()

	_, err := b.GetEscrow("1")
	require.Error(t, err)
	require.False(t, errors.Is(err, devbridge.ErrEscrowNotFound))
	require.Contains(t, err.Error(), "GetDevshardEscrow")
}

// ── GetHostInfo ──────────────────────────────────────────────────────────────

func TestGRPCBridge_GetHostInfo_MapsFields(t *testing.T) {
	fake := &fakeMockChain{
		participant: &mockchainpb.GetParticipantResponse{
			Address: "gonka1host",
			Url:     "http://devshardd-testenv-0:8080",
		},
	}
	b, cleanup := newTestBridge(t, fake)
	defer cleanup()

	got, err := b.GetHostInfo("gonka1host")
	require.NoError(t, err)
	require.Equal(t, "gonka1host", got.Address)
	require.Equal(t, "http://devshardd-testenv-0:8080", got.URL)
}

func TestGRPCBridge_GetHostInfo_NotFoundReturnsSentinel(t *testing.T) {
	fake := &fakeMockChain{participantError: status.Error(codes.NotFound, "nope")}
	b, cleanup := newTestBridge(t, fake)
	defer cleanup()

	_, err := b.GetHostInfo("gonka1unknown")
	require.ErrorIs(t, err, devbridge.ErrParticipantNotFound)
}

// ── VerifyWarmKey ────────────────────────────────────────────────────────────

func TestGRPCBridge_VerifyWarmKey_TrueWhenInGrantees(t *testing.T) {
	fake := &fakeMockChain{
		grantees: &mockchainpb.GetGranteesResponse{
			Addresses: []string{"gonka1a", "gonka1warm", "gonka1c"},
		},
	}
	b, cleanup := newTestBridge(t, fake)
	defer cleanup()

	ok, err := b.VerifyWarmKey("gonka1warm", "gonka1validator")
	require.NoError(t, err)
	require.True(t, ok)
}

func TestGRPCBridge_VerifyWarmKey_FalseWhenNotInGrantees(t *testing.T) {
	fake := &fakeMockChain{
		grantees: &mockchainpb.GetGranteesResponse{
			Addresses: []string{"gonka1a", "gonka1b"},
		},
	}
	b, cleanup := newTestBridge(t, fake)
	defer cleanup()

	ok, err := b.VerifyWarmKey("gonka1stranger", "gonka1validator")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestGRPCBridge_VerifyWarmKey_NotFoundTreatedAsFalse(t *testing.T) {
	fake := &fakeMockChain{granteesError: status.Error(codes.NotFound, "nope")}
	b, cleanup := newTestBridge(t, fake)
	defer cleanup()

	ok, err := b.VerifyWarmKey("gonka1warm", "gonka1validator")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestGRPCBridge_VerifyWarmKey_CacheHitsSkipServer(t *testing.T) {
	fake := &fakeMockChain{
		grantees: &mockchainpb.GetGranteesResponse{
			Addresses: []string{"gonka1warm"},
		},
	}
	b, cleanup := newTestBridge(t, fake)
	defer cleanup()

	// First call populates the cache and makes one server round trip.
	ok, err := b.VerifyWarmKey("gonka1warm", "gonka1validator")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int32(1), atomic.LoadInt32(&fake.granteesCalls))

	// Second call with identical args must be served from cache.
	ok, err = b.VerifyWarmKey("gonka1warm", "gonka1validator")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int32(1), atomic.LoadInt32(&fake.granteesCalls))

	// A different validator address misses the cache and hits the server again.
	ok, err = b.VerifyWarmKey("gonka1warm", "gonka1other")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int32(2), atomic.LoadInt32(&fake.granteesCalls))
}

// ── Stubs ────────────────────────────────────────────────────────────────────
//
// The bridge must refuse every notification / action method the mock-chain
// cannot implement — accidental invocation from HostManager should surface
// loudly rather than silently succeed. Matches devshard/bridge.RESTBridge.

func TestGRPCBridge_Stubs_ReturnNotImplemented(t *testing.T) {
	b, cleanup := newTestBridge(t, &fakeMockChain{})
	defer cleanup()

	require.ErrorIs(t, b.OnEscrowCreated(devbridge.EscrowInfo{EscrowID: "1"}), devbridge.ErrNotImplemented)
	require.ErrorIs(t, b.OnSettlementProposed("1", []byte{0x01}, 1), devbridge.ErrNotImplemented)
	require.ErrorIs(t, b.OnSettlementFinalized("1"), devbridge.ErrNotImplemented)
	require.ErrorIs(t, b.SubmitDisputeState("1", []byte{0x01}, 1, map[uint32][]byte{0: {0x02}}), devbridge.ErrNotImplemented)
}

// ── Contract ─────────────────────────────────────────────────────────────────

func TestGRPCBridge_SatisfiesMainnetBridge(t *testing.T) {
	b, cleanup := newTestBridge(t, &fakeMockChain{})
	defer cleanup()

	// Runtime assertion complementing the package-level compile-time
	// var _ devbridge.MainnetBridge = (*GRPCBridge)(nil) check: a
	// failing cast here would already have failed the build, but the
	// test makes the contract obvious in CI output.
	var iface devbridge.MainnetBridge = b
	require.NotNil(t, iface)
}

// ── Input validation ─────────────────────────────────────────────────────────

func TestGRPCBridge_NewGRPCBridge_RejectsEmptyTarget(t *testing.T) {
	_, err := testenvbridge.NewGRPCBridge(context.Background(), "")
	require.Error(t, err)
}

func TestGRPCBridge_NewGRPCBridge_RejectsNilContext(t *testing.T) {
	// Bind nil via a variable so staticcheck SA1012 (which flags literal
	// nil context arguments) does not fire; the intent here is to
	// exercise the constructor's defensive check, not propagate a nil
	// ctx to downstream code.
	var ctx context.Context
	_, err := testenvbridge.NewGRPCBridge(ctx, "mock-chain:9090")
	require.Error(t, err)
}
