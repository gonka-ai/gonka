package inference

import (
	mlnodeclient "common/nodemanager"
	nmgen "common/nodemanager/gen"
	"context"
	"devshard"
	"devshard/bridge"
	"devshard/observability"
	"devshard/storage"
	"fmt"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestValidationEpochBeforeLeaseAndFetch(t *testing.T) {
	for _, epoch := range []uint64{0, 415, 417} {
		t.Run(fmt.Sprint(epoch), func(t *testing.T) {
			phase := testPhase(epoch)
			v := &Validator{phase: phase}
			c := NewLeaseValidator(v, phase, &stubLeases{}, "host", time.Hour)
			require.False(t, devshard.CanValidateEpoch(c, 416))
			_, err := c.Validate(context.Background(), devshard.ValidateRequest{EpochID: 416})
			require.ErrorIs(t, err, devshard.ErrValidationEpochUnavailable)
			_, err = v.Validate(context.Background(), devshard.ValidateRequest{EpochID: 416})
			require.ErrorIs(t, err, devshard.ErrValidationEpochUnavailable)
		})
	}
}

func TestValidationEpochFixedAcrossLeaseAcquire(t *testing.T) {
	phase := testPhase(416)
	store := storage.NewMemory()
	c := NewLeaseValidator(&stubValidator{fn: func(_ context.Context, req devshard.ValidateRequest) (*devshard.ValidateResult, error) {
		require.Equal(t, uint64(416), req.EpochID)
		phase.SetEpoch(417)
		return &devshard.ValidateResult{Valid: true}, nil
	}}, phase, store, "host", time.Hour)
	_, err := c.Validate(context.Background(), devshard.ValidateRequest{EscrowID: "e", InferenceID: 1})
	require.NoError(t, err)
	require.ErrorIs(t, c.AllowValidationSubmit(context.Background(), "e", 1), devshard.ErrValidationEpochUnavailable)
	_, remembered := c.acquired("e", 1)
	require.False(t, remembered)
	owned, err := store.OwnsPendingLease(context.Background(), "e", 1, 416, "host")
	require.NoError(t, err)
	require.True(t, owned)
	owned, err = store.OwnsPendingLease(context.Background(), "e", 1, 417, "host")
	require.NoError(t, err)
	require.False(t, owned)
}

func TestValidationEpochAfterNodeAcquireRefunds(t *testing.T) {
	phase := testPhase(416)
	hits, releases := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(context.Context, *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			phase.SetEpoch(417)
			return &nmgen.AcquireMLNodeResponse{LockId: "lock", NodeId: "node", Endpoint: srv.URL}, nil
		},
		releaseFunc: func(_ context.Context, req *nmgen.ReleaseMLNodeRequest) (*nmgen.ReleaseMLNodeResponse, error) {
			require.Equal(t, nmgen.ReleaseOutcome_SUCCESS, req.Outcome)
			releases++
			return &nmgen.ReleaseMLNodeResponse{}, nil
		},
	})
	e := newTestEngine(ml, nil, nil)
	e.validationBudget.earn("m")
	v := &Validator{engine: e, phase: phase}
	_, err := v.executeMLRequest(context.Background(), "m", "e", []byte(`{}`), 416)
	require.ErrorIs(t, err, devshard.ErrValidationEpochUnavailable)
	require.Zero(t, hits)
	require.Equal(t, 1, releases)
	require.True(t, e.validationBudget.available("m"))
}

func TestValidationEpochFallbackPropagates(t *testing.T) {
	ml := startEngineMLClient(t, &engineMockNM{acquireFunc: func(context.Context, *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
		return nil, status.Error(codes.Unavailable, "offline")
	}})
	mgr := mlnodeclient.NewManager(time.Hour)
	mgr.Observe("m", "node", "http://node")
	e := newTestEngine(ml, mgr, nil)
	e.validationBudget.earn("m")
	checks := 0
	_, err := e.doWithLockedNode(context.Background(), observability.PathValidate, "m", "e", func(string, func()) (*http.Response, error) { t.Fatal("must not dispatch"); return nil, nil }, func() error {
		checks++
		if checks == 3 {
			return devshard.ErrValidationEpochUnavailable
		}
		return nil
	})
	require.ErrorIs(t, err, devshard.ErrValidationEpochUnavailable)
	require.Equal(t, 3, checks)
	require.True(t, e.validationBudget.available("m"))
}

func TestValidationNilPhaseDefers(t *testing.T) {
	v := &Validator{}
	c := NewLeaseValidator(v, nil, &stubLeases{}, "host", time.Hour)
	for _, engine := range []devshard.ValidationEngine{v, c} {
		for _, epoch := range []uint64{0, 416} {
			require.False(t, devshard.CanValidateEpoch(engine, epoch))
			_, err := engine.Validate(context.Background(), devshard.ValidateRequest{EpochID: epoch})
			require.ErrorIs(t, err, devshard.ErrValidationEpochUnavailable)
		}
	}
}

type epochAdvanceBridge struct {
	bridge.MainnetBridge
	advance func()
}

func (b epochAdvanceBridge) GetHostInfo(string) (*bridge.HostInfo, error) {
	b.advance()
	return &bridge.HostInfo{URL: "http://executor"}, nil
}

func TestValidationEpochAfterExecutorLookup(t *testing.T) {
	phase := testPhase(416)
	v := &Validator{phase: phase, bridge: epochAdvanceBridge{advance: func() { phase.SetEpoch(417) }}}
	// A nil recorder makes any payload signing/fetching after lookup fail the test.
	_, err := v.Validate(context.Background(), devshard.ValidateRequest{EpochID: 416})
	require.ErrorIs(t, err, devshard.ErrValidationEpochUnavailable)
}
