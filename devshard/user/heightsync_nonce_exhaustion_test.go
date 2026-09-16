package user

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

// ackBlackholeClient delivers diffs normally but drops every height acknowledgement.
// Other responses, signatures and host errors still reach the gateway.
type ackBlackholeClient struct {
	HostClient
	limitErrors *atomic.Uint64
}

func (c ackBlackholeClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receipt func(*host.HostResponse)) (*host.HostResponse, error) {
	drop := func(resp *host.HostResponse) {
		if resp == nil {
			return
		}
		filtered := make([]*types.DevshardTx, 0, len(resp.Mempool))
		for _, tx := range resp.Mempool {
			if tx.GetHeightAck() == nil {
				filtered = append(filtered, tx)
			}
		}
		resp.Mempool = filtered
	}
	resp, err := c.HostClient.Send(ctx, req, stream, func(resp *host.HostResponse) {
		drop(resp)
		if receipt != nil {
			receipt(resp)
		}
	})
	drop(resp)
	if errors.Is(err, types.ErrNonceLimitExceeded) {
		c.limitErrors.Add(1)
	}
	return resp, err
}

// This reproducer intentionally asserts the current vulnerable behavior. Once
// heartbeat nonce budgeting is fixed, replace exhaustion with the desired bound.
func TestHeartbeat_NonceExhaustionFromSnapshot(t *testing.T) {
	const (
		escrowID  = "escrow-1"
		numHosts  = 16
		nearLimit = uint64(19_970)
		maxNonce  = uint32(20_000)
		balance   = uint64(10_000_000)
		fee       = uint64(7)
	)
	store := newTestStore(t)
	keys := make([]*signing.Secp256k1Signer, numHosts)
	for i := range keys {
		keys[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(keys)
	config := testutil.DefaultConfig(numHosts)
	config.FeePerNonce = fee
	verifier := signing.NewSecp256k1Verifier()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID: escrowID, Version: testutil.RuntimeTestVersion,
		CreatorAddr: user.Address(), Config: config, Group: group, InitialBalance: balance,
	}))
	oracle := &sessionOracle{hash: []byte{0xaa}}
	oracle.height.Store(100)
	clients := make([]HostClient, numHosts)
	for i := range keys {
		sm := newTestStateMachine(t, escrowID, config, group, balance, user.Address(), verifier)
		h, err := host.NewHost(sm, keys[i], stub.NewInferenceEngine(), escrowID, group, nil,
			host.WithGrace(100), host.WithChainOracle(oracle))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}
	now := time.Unix(1_000, 0)
	sm := newTestStateMachine(t, escrowID, config, group, balance, user.Address(), verifier)
	session, err := NewSession(sm, user, escrowID, group, clients, verifier,
		WithStorage(store), WithHeightSyncCadence(16, numHosts),
		WithHeartbeatClock(func() time.Time { return now }))
	require.NoError(t, err)
	seedFloorByInference(t, session)

	// Fast-forward only the historical prefix. Keep the real bootstrap journal
	// so recovery reconstructs its host-signed floor. The marker advances storage
	// metadata; recovery starts after the snapshot and never applies this marker.
	snapshot := sm.ExportState()
	snapshot.LatestNonce = nearLimit
	cursors := make(map[int]uint64, numHosts)
	for i := range keys {
		cursors[i] = nearLimit
	}
	require.NoError(t, store.AppendDiff(escrowID, types.DiffRecord{Diff: types.Diff{Nonce: nearLimit}}))
	require.NoError(t, writeSnapshotErr(store, escrowID, nearLimit, snapshot, cursors,
		sm.ExportCommittedEntries(), sm.ExportSealedNonces(), sm.ExportHeightSyncFloor()))

	// Recover each host from the same persisted snapshot, with the chain limit
	// enabled. Separate state machines retain independent host state.
	var limitErrors atomic.Uint64
	for i := range keys {
		_, hostSM, err := RecoverSession(store, user, verifier, escrowID, testutil.RuntimeTestVersion, group, clients)
		require.NoError(t, err)
		h, err := host.NewHost(hostSM, keys[i], stub.NewInferenceEngine(), escrowID, group, nil,
			host.WithGrace(100), host.WithChainOracle(oracle),
			host.WithMaxNonceProvider(devshard.StaticMaxNonce(maxNonce)))
		require.NoError(t, err)
		clients[i] = ackBlackholeClient{HostClient: &InProcessClient{Host: h}, limitErrors: &limitErrors}
	}
	session, sm, err = RecoverSession(store, user, verifier, escrowID, testutil.RuntimeTestVersion, group, clients)
	require.NoError(t, err)
	session.SetHeightSyncCadence(16, numHosts)
	session.clock = func() time.Time { return now }
	require.Equal(t, nearLimit, session.Nonce())
	floor, _, known := sm.HeightSyncFloorAsOf(nearLimit + 1)
	require.True(t, known)
	require.Equal(t, uint64(100), floor)
	before := sm.SnapshotState()

	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	require.Equal(t, nearLimit+numHosts+1, session.Nonce())
	require.Zero(t, limitErrors.Load())
	require.Zero(t, session.HeartbeatTurnovers())
	// Advance the injected clock instead of waiting through an idle timeout.
	now = now.Add(session.heartbeat.Config().TurnTimeout)
	err = session.MaybeHeartbeat(context.Background())
	require.NoError(t, err, "heartbeat currently suppresses host send errors")
	require.Positive(t, limitErrors.Load(), "hosts must reject with ErrNonceLimitExceeded")
	require.Equal(t, nearLimit+2*(numHosts+1), session.Nonce())
	require.Greater(t, session.Nonce(), uint64(maxNonce))
	require.Zero(t, session.HeartbeatTurnovers())
	after := sm.SnapshotState()
	consumed := session.Nonce() - nearLimit
	require.Equal(t, consumed*fee, after.Fees-before.Fees)
	require.Equal(t, consumed*fee, before.Balance-after.Balance)
	t.Logf("nonce %d -> %d; fees charged: %d; simulated elapsed: %s", nearLimit, session.Nonce(), after.Fees-before.Fees, now.Sub(time.Unix(1_000, 0)))
}
