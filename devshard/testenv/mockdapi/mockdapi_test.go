package mockdapi_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/blockoracle"
	"devshard/blockoracle/observer"
	"devshard/blockoracle/server"
	"devshard/signing"
	"devshard/testenv/mockdapi"

	nmpb "devshard/mlnode/gen"
)

// --- Shared test rig: real blockoracle producer over httptest. ---
//
// mockdapi.New builds a trust-mode HTTP client; wiring it against a
// real observer+server stack is the most faithful way to test the
// subscribe + Latest round-trip. Pinned to 3 validators (instead of
// the canonical 10) because trust mode does not verify signatures and
// the test only cares about the header-delivery pipe.

func newProducer(t *testing.T) (*httptest.Server, *observer.Mock, func()) {
	t.Helper()

	mocks := make([]observer.MockValidator, 0, 3)
	for i := 0; i < 3; i++ {
		s, err := signing.GenerateKey()
		require.NoError(t, err)
		addr, err := blockoracle.AddressBytes(s.PublicKeyBytes())
		require.NoError(t, err)
		mocks = append(mocks, observer.MockValidator{Signer: s, Address: addr, Power: 1})
	}

	mock, err := observer.NewMock(observer.MockConfig{
		ChainID:       "mockdapi-test",
		Validators:    mocks,
		BlockInterval: 50 * time.Millisecond,
		Seed:          1,
		Start:         time.Unix(1_700_000_000, 0).UTC(),
		InitialHeight: 1,
	})
	require.NoError(t, err)

	e := echo.New()
	server.Mount(e.Group(""), mock)
	ts := httptest.NewServer(e)
	return ts, mock, func() { ts.Close() }
}

// --- Tests: mockdapi.New ---

func TestNew_RequiresHeightSyncURL(t *testing.T) {
	// A misconfigured caller must get an explicit error instead of a
	// broken handle that fails asynchronously on first Latest().
	_, err := mockdapi.New(context.Background(), mockdapi.Config{
		HeightSyncURL: "",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "HeightSyncURL")
}

func TestNew_RejectsNilContext(t *testing.T) {
	// Bind nil via a variable so staticcheck SA1012 does not fire; the
	// intent is to exercise the constructor's defensive guard, not
	// propagate nil ctx downstream.
	var ctx context.Context
	_, err := mockdapi.New(ctx, mockdapi.Config{HeightSyncURL: "http://x"})
	require.Error(t, err)
}

func TestNew_BuildsOracleAndNodeManager(t *testing.T) {
	ts, mock, cleanup := newProducer(t)
	defer cleanup()

	// Advance once before construction so Latest has something to
	// return on the initial fetch path (trust-mode client falls back
	// to a synchronous GET /block/latest if the SSE hasn't caught up).
	_, err := mock.AdvanceOne()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	md, err := mockdapi.New(ctx, mockdapi.Config{
		HeightSyncURL:    ts.URL,
		ChainID:          "mockdapi-test",
		ResubscribeAfter: 20 * time.Millisecond,
		StaleAfter:       time.Second,
	})
	require.NoError(t, err)
	require.NotNil(t, md)
	defer md.Close()

	// Oracle must expose a usable blockoracle.BlockOracle surface.
	require.NotNil(t, md.Oracle)
	var _ blockoracle.BlockOracle = md.Oracle

	h, err := md.Oracle.Latest(ctx)
	require.NoError(t, err)
	require.NotNil(t, h)
	require.Equal(t, int64(1), h.Height)
	require.Equal(t, "mockdapi-test", h.ChainID)
	// Trust mode must forward the full signature vector for downstream
	// settlement proofs. The producer signs every block with every
	// validator unless the mock drops some; at minimum we expect 1.
	require.NotEmpty(t, h.Commit.Signatures,
		"trust-mode client must cache full Commit.Signatures for downstream proofs")

	// NodeManager must satisfy the prod gen.NodeManagerClient shape.
	require.NotNil(t, md.NodeManager)
	var _ nmpb.NodeManagerClient = md.NodeManager
}

func TestNew_OracleReflectsLiveProducerAdvances(t *testing.T) {
	// Once the oracle's SSE loop is running, new heights must appear
	// on Latest() without additional round trips initiated by the
	// caller. This guards the wiring from accidentally turning the
	// client into a pull-based polling consumer.
	ts, mock, cleanup := newProducer(t)
	defer cleanup()

	_, err := mock.AdvanceOne()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	md, err := mockdapi.New(ctx, mockdapi.Config{
		HeightSyncURL:    ts.URL,
		ResubscribeAfter: 20 * time.Millisecond,
		StaleAfter:       time.Second,
	})
	require.NoError(t, err)
	defer md.Close()

	// Prime the oracle so Latest is non-nil and the SSE loop is live.
	h1, err := md.Oracle.Latest(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), h1.Height)

	// Advance the producer; wait for the SSE push to land. 500ms is
	// generous vs 50ms block interval + 20ms resubscribe.
	_, err = mock.AdvanceOne()
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		h, err := md.Oracle.Latest(ctx)
		return err == nil && h != nil && h.Height >= 2
	}, 500*time.Millisecond, 10*time.Millisecond)
}

// --- Tests: MockDapi.Close ---

func TestClose_IsIdempotent(t *testing.T) {
	ts, _, cleanup := newProducer(t)
	defer cleanup()

	ctx := context.Background()
	md, err := mockdapi.New(ctx, mockdapi.Config{HeightSyncURL: ts.URL})
	require.NoError(t, err)

	md.Close()
	require.NotPanics(t, func() { md.Close() },
		"Close must be safe to call twice; scenarios often close both on defer and via context cancel")
}

func TestClose_OnNilReceiverIsSafe(t *testing.T) {
	// Defensive: wiring code that builds a MockDapi conditionally may
	// call Close on a nil pointer in error paths. Guard against the
	// panic.
	var md *mockdapi.MockDapi
	require.NotPanics(t, func() { md.Close() })
}

// --- Tests: NoopNodeManager ---

func TestNoopNodeManager_AcquireReturnsSyntheticLockAndIncrements(t *testing.T) {
	// Lock ids must be unique per call so tests that map lock → outcome
	// can distinguish concurrent acquisitions. Endpoint and node id
	// are fixed placeholders; scenarios that care about heterogeneous
	// nodes subclass or replace this stub.
	n := mockdapi.NewNoopNodeManager()
	ctx := context.Background()

	r1, err := n.AcquireMLNode(ctx, &nmpb.AcquireMLNodeRequest{Model: "llama"})
	require.NoError(t, err)
	require.NotEmpty(t, r1.LockId)
	require.Equal(t, "mockdapi://noop", r1.Endpoint)
	require.Equal(t, "mockdapi-node-0", r1.NodeId)

	r2, err := n.AcquireMLNode(ctx, &nmpb.AcquireMLNodeRequest{Model: "llama"})
	require.NoError(t, err)
	require.NotEqual(t, r1.LockId, r2.LockId,
		"AcquireMLNode must hand out distinct lock ids so callers can disambiguate concurrent acquisitions")

	require.Equal(t, uint64(2), n.AcquireCount())
	require.Equal(t, uint64(0), n.ReleaseCount())
}

func TestNoopNodeManager_AcquireNeverExhausts(t *testing.T) {
	// The testenv has "infinite" capacity by design; a ResourceExhausted
	// error here would silently fail scenarios that stress-test the
	// host's acquire loop. Iterate a lot and assert no error ever.
	n := mockdapi.NewNoopNodeManager()
	ctx := context.Background()
	for i := 0; i < 1024; i++ {
		_, err := n.AcquireMLNode(ctx, &nmpb.AcquireMLNodeRequest{Model: "llama"})
		require.NoError(t, err)
	}
	require.Equal(t, uint64(1024), n.AcquireCount())
}

func TestNoopNodeManager_ReleaseAcceptsAnyOutcome(t *testing.T) {
	// Release must succeed for every ReleaseOutcome value; the testenv
	// never penalizes, so APPLICATION_ERROR / TIMEOUT must not bubble
	// back as a gRPC error either.
	n := mockdapi.NewNoopNodeManager()
	ctx := context.Background()

	outcomes := []nmpb.ReleaseOutcome{
		nmpb.ReleaseOutcome_SUCCESS,
		nmpb.ReleaseOutcome_TRANSPORT_ERROR,
		nmpb.ReleaseOutcome_APPLICATION_ERROR,
		nmpb.ReleaseOutcome_TIMEOUT,
	}
	for _, oc := range outcomes {
		_, err := n.ReleaseMLNode(ctx, &nmpb.ReleaseMLNodeRequest{
			LockId:  "some-lock",
			Outcome: oc,
		})
		require.NoError(t, err, "release must succeed for outcome %v", oc)
	}
	require.Equal(t, uint64(len(outcomes)), n.ReleaseCount())
}

func TestNoopNodeManager_HonorsContextCancellation(t *testing.T) {
	// Cancellation must propagate; scenarios that cancel during shutdown
	// rely on Acquire/Release returning promptly instead of hanging.
	n := mockdapi.NewNoopNodeManager()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := n.AcquireMLNode(ctx, &nmpb.AcquireMLNodeRequest{Model: "llama"})
	require.ErrorIs(t, err, context.Canceled)

	_, err = n.ReleaseMLNode(ctx, &nmpb.ReleaseMLNodeRequest{LockId: "x"})
	require.ErrorIs(t, err, context.Canceled)
}

func TestNoopNodeManager_SatisfiesGenContract(t *testing.T) {
	// Runtime cross-check: if the generated gRPC interface gains a
	// method, this assertion fails before the stub is silently dropped
	// from the contract. Complements the compile-time assertion in
	// nodemanager.go.
	var _ nmpb.NodeManagerClient = mockdapi.NewNoopNodeManager()
}
