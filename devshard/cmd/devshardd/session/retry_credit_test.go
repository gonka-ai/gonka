package session

import (
	"common/chain"
	"context"
	"testing"
	"time"

	devshardpkg "devshard"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/transport"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

type creditRetryManager struct{ server *transport.Server }

func (m creditRetryManager) ActiveEscrowIDs() []string                       { return []string{"escrow-1"} }
func (m creditRetryManager) existingServer(string) (*transport.Server, bool) { return m.server, true }

type creditRetryEngine struct {
	credits   int
	attempted []string
}

func (e *creditRetryEngine) CanValidate(model string) bool { return model == "ready" && e.credits > 0 }
func (e *creditRetryEngine) Validate(_ context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	e.attempted = append(e.attempted, req.Model)
	if !e.CanValidate(req.Model) {
		return nil, devshardpkg.ErrValidationDeferred
	}
	e.credits--
	return &devshardpkg.ValidateResult{Valid: true}, nil
}
func TestV4RetryCreditsPreservePendingAndContinueOtherModels(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{signer, testutil.MustGenerateKey(t)})
	cfg := testutil.DefaultConfig(2)
	store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), cfg, group, 100000)
	sm, err := state.NewStateMachine("escrow-1", cfg, group, 100000, user.Address(), signing.NewSecp256k1Verifier(), store)
	require.NoError(t, err)
	snapshot := sm.SnapshotState()
	snapshot.Inferences[1] = &types.InferenceRecord{ExecutorSlot: 1, Model: "empty", Status: types.StatusFinished}
	snapshot.Inferences[2] = &types.InferenceRecord{ExecutorSlot: 1, Model: "ready", Status: types.StatusFinished}
	sm.RestoreState(&snapshot)
	h, err := host.NewHost(sm, signer, stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithEpochID(1))
	require.NoError(t, err)
	srv, err := transport.NewServer(h, store, signing.NewSecp256k1Verifier(), user.Address())
	require.NoError(t, err)
	claims := 0
	leases := &stubStaleLeaseStore{acquireFn: func(context.Context, string, string, time.Duration) (uint64, uint64, error) {
		claims++
		if claims <= 2 {
			return uint64(claims), 1, nil
		}
		return 0, 0, nil
	}}
	engine := &creditRetryEngine{}
	phase := new(chain.Phase)
	phase.SetEpoch(1)
	loop := &RetryLoop{phase: phase, leases: leases, inner: engine, manager: creditRetryManager{srv}, instanceAddr: signer.Address(), leaseTTL: DefaultLeaseTTL}
	loop.retryForEscrow(context.Background(), "escrow-1")
	require.Zero(t, claims)
	engine.credits = 1
	loop.retryForEscrow(context.Background(), "escrow-1")
	require.Equal(t, []string{"empty", "ready"}, engine.attempted)
	require.Equal(t, 2, claims)
	require.Len(t, leases.setResultCalls, 1)
	require.Contains(t, leases.setResultCalls[0], "escrow-1/2/submitted")
}
