package user

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks"
	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/stub"
	"devshard/types"
)

type sessionOracle struct {
	height atomic.Int64
	hash   []byte
	err    error
}

func (o *sessionOracle) Latest(context.Context) (*blocks.Header, error) {
	if o.err != nil {
		return nil, o.err
	}
	h := o.height.Load()
	return blocks.HashOnlyHeader(h, time.Unix(1, 0).UTC(), "fake-chain", append([]byte(nil), o.hash...)), nil
}
func (o *sessionOracle) At(ctx context.Context, _ int64) (*blocks.Header, error) {
	return o.Latest(ctx)
}
func (o *sessionOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}
func (o *sessionOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func TestUser_ForceHeightSyncTurn_AppearsOnlyInTriggerDiff(t *testing.T) {
	const numHosts = 3
	const k = uint64(10)
	const slots = uint64(numHosts)
	session, _, _ := setupSessionWithOptions(t, numHosts, 100000, 100, WithHeightSyncCadence(k, slots))

	ctx := context.Background()
	base := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}
	forced := base
	forced.ForceHeightSyncAnchor = true

	countForceTxs := func(d types.Diff) int {
		n := 0
		for _, tx := range d.Txs {
			if tx.GetForceHeightSyncTurn() != nil {
				n++
			}
		}
		return n
	}
	findForceTx := func(d types.Diff) *types.MsgForceHeightSyncTurn {
		for _, tx := range d.Txs {
			if inner := tx.GetForceHeightSyncTurn(); inner != nil {
				return inner
			}
		}
		return nil
	}

	_, err := session.SendInference(ctx, forced)
	require.NoError(t, err, "trigger inference at nonce 1")
	require.Equal(t, uint64(1), session.Nonce())

	for n := 2; n <= int(slots); n++ {
		_, err = session.SendInference(ctx, forced)
		require.NoError(t, err, "in-window inference at nonce %d", n)
	}
	require.Equal(t, slots, session.Nonce(), "session advanced through the full forced window")

	diffs := session.Diffs()
	require.Len(t, diffs, int(slots))
	require.Equal(t, 1, countForceTxs(diffs[0]),
		"trigger diff at nonce 1 must contain exactly one MsgForceHeightSyncTurn")
	tx := findForceTx(diffs[0])
	require.NotNil(t, tx, "trigger diff must carry the force-turn tx")
	require.Equal(t, uint64(1), tx.TriggerNonce)
	require.Equal(t, slots, tx.SlotsNum)
	require.Equal(t, slots, tx.EndNonce)
	require.Equal(t, k, tx.AnchorK)

	for i := 1; i < int(slots); i++ {
		require.Equal(t, 0, countForceTxs(diffs[i]),
			"diff at nonce %d (in-window) must NOT contain MsgForceHeightSyncTurn", diffs[i].Nonce)
	}

	require.True(t, session.StateMachine().HeightSyncForcedTurnActive(slots),
		"sanity: forced turn still active at the last in-window nonce")
	require.False(t, session.StateMachine().HeightSyncForcedTurnActive(slots+1),
		"sanity: forced turn closed for the next nonce")

	_, err = session.SendInference(ctx, forced)
	require.NoError(t, err, "next forced trigger after window closes")
	diffs = session.Diffs()
	require.Len(t, diffs, int(slots)+1)
	require.Equal(t, 1, countForceTxs(diffs[int(slots)]),
		"a fresh ForceHeightSyncAnchor after the previous window closes must re-open a new turn")
	tx2 := findForceTx(diffs[int(slots)])
	require.NotNil(t, tx2)
	require.Equal(t, slots+1, tx2.TriggerNonce)
	require.Equal(t, 2*slots, tx2.EndNonce)
}

func setupHeartbeatSession(t *testing.T, height *uint64) *Session {
	t.Helper()
	return setupHeartbeatSessionWithOracles(t, height, nil)
}

func setupHeartbeatSessionWithOracles(t *testing.T, height *uint64, oracles []blocks.BlockOracle) *Session {
	t.Helper()
	const numHosts = 3
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		opts := []host.HostOption{host.WithGrace(100)}
		if i < len(oracles) && oracles[i] != nil {
			opts = append(opts, host.WithChainOracle(oracles[i]))
		}
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, opts...)
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier,
		WithHeightSyncCadence(10, uint64(numHosts)),
		WithObservedHeight(func() (uint64, []byte, bool) {
			h := *height
			if h == 0 {
				return 0, nil, false
			}
			return h, []byte{0xaa}, true
		}),
	)
	require.NoError(t, err)
	return session
}

func heightAcksInDiffs(diffs []types.Diff) []*types.MsgHeightAck {
	var out []*types.MsgHeightAck
	for _, d := range diffs {
		for _, tx := range d.Txs {
			if ack := tx.GetHeightAck(); ack != nil {
				out = append(out, ack)
			}
		}
	}
	return out
}

func TestHeartbeat_QuietSessionOpensTurn(t *testing.T) {
	var height uint64 = 100
	session := setupHeartbeatSession(t, &height)
	ctx := context.Background()

	require.NoError(t, session.MaybeHeartbeat(ctx))
	diffs := session.Diffs()
	require.GreaterOrEqual(t, len(diffs), 3, "slots_num heartbeat diffs")
	force := diffs[0].Txs[0].GetForceHeightSyncTurn()
	require.NotNil(t, force)
	require.Equal(t, uint64(1), force.TriggerNonce)
	require.Equal(t, "heartbeat", force.Reason)
	hb := diffs[0].Txs[1].GetHeartbeat()
	require.NotNil(t, hb)
	require.Equal(t, uint64(100), hb.ObservedHeight)

	rec := session.HeartbeatTurnTracker().Record(1)
	require.NotNil(t, rec)
	require.Equal(t, uint64(1), rec.TurnSeq)
	require.Equal(t, uint64(100), rec.HReq)
}

func TestHeartbeat_NoObservedHeightSkips(t *testing.T) {
	var height uint64
	session := setupHeartbeatSession(t, &height)
	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	require.Empty(t, session.Diffs())
	require.Equal(t, 1, session.HeartbeatSkippedNoHeight())
	require.Equal(t, uint64(0), session.Nonce())
}

func TestHeartbeat_SpanDispatchAddressesEverySlot(t *testing.T) {
	var height uint64 = 100
	session := setupHeartbeatSession(t, &height)
	require.NoError(t, session.MaybeHeartbeat(context.Background()))

	diffs := session.Diffs()
	const slots = 3
	require.GreaterOrEqual(t, len(diffs), slots)
	span := diffs[:slots]
	seen := map[uint32]int{}
	for i, d := range span {
		require.Equal(t, uint64(i+1), d.Nonce)
		var hb *types.MsgHeartbeat
		for _, tx := range d.Txs {
			if inner := tx.GetHeartbeat(); inner != nil {
				hb = inner
			}
			require.Nil(t, tx.GetHeightAck(), "span must not wait for acks")
		}
		require.NotNil(t, hb, "diff %d missing MsgHeartbeat", d.Nonce)
		require.Equal(t, uint64(1), hb.TurnSeq)
		require.Equal(t, uint64(slots), hb.SlotsNum)
		seen[uint32(d.Nonce%slots)]++
	}
	require.Len(t, seen, slots)
	for slot, n := range seen {
		require.Equal(t, 1, n, "slot %d", slot)
	}
	require.Equal(t, 1, countHeartbeatForce(span[0]))
	for i := 1; i < slots; i++ {
		require.Equal(t, 0, countHeartbeatForce(span[i]))
	}
	// MinRoundsPerBlock=2 → one ack-flush nonce after the span.
	require.GreaterOrEqual(t, len(diffs), slots+1)
}

func TestHeartbeat_AckInclusionAndSyncVectorPrevTurn(t *testing.T) {
	var height uint64 = 100
	session := setupHeartbeatSession(t, &height)
	ctx := context.Background()
	require.NoError(t, session.MaybeHeartbeat(ctx))

	diffs := session.Diffs()
	const slots = 3
	require.GreaterOrEqual(t, len(diffs), slots+1)
	for _, d := range diffs[:slots] {
		for _, tx := range d.Txs {
			require.Nil(t, tx.GetHeightAck(), "span must not wait for acks")
		}
	}
	acks := heightAcksInDiffs(diffs)
	require.Len(t, acks, slots, "flush round must include one host ack per slot")
	seen := map[uint32]types.SyncState{}
	for _, ack := range acks {
		require.Equal(t, uint64(1), ack.TurnSeq)
		require.Equal(t, types.SyncState_ORACLE_UNAVAILABLE, ack.SyncState)
		seen[ack.SlotId] = ack.SyncState
	}
	require.Len(t, seen, slots)

	rec := session.HeartbeatTurnTracker().Record(1)
	require.Equal(t, heightsync.TurnOpen, rec.State, "ORACLE_UNAVAILABLE does not count toward Q")

	height = 102
	require.NoError(t, session.MaybeHeartbeat(ctx))
	require.Equal(t, heightsync.TurnDegraded, session.HeartbeatTurnTracker().Record(1).State)

	var hb *types.MsgHeartbeat
	for _, d := range session.Diffs() {
		for _, tx := range d.Txs {
			if inner := tx.GetHeartbeat(); inner != nil && inner.TurnSeq == 2 {
				hb = inner
			}
		}
	}
	require.NotNil(t, hb)
	require.Len(t, hb.SyncVector, 3)
	require.Equal(t, types.AckStatus_ACKED, hb.SyncVector[0].Status)
	require.Equal(t, types.AckStatus_ACKED, hb.SyncVector[1].Status)
	require.Equal(t, types.AckStatus_ACKED, hb.SyncVector[2].Status)
}

func TestHeartbeat_LiveHostsQuorumCompletes(t *testing.T) {
	var height uint64 = 100
	oracles := make([]blocks.BlockOracle, 3)
	for i := range oracles {
		oracles[i] = &sessionOracle{hash: []byte{0xaa}}
		oracles[i].(*sessionOracle).height.Store(100)
	}
	session := setupHeartbeatSessionWithOracles(t, &height, oracles)
	require.NoError(t, session.MaybeHeartbeat(context.Background()))

	acks := heightAcksInDiffs(session.Diffs())
	require.Len(t, acks, 3)
	for _, ack := range acks {
		require.Equal(t, types.SyncState_SYNCED, ack.SyncState)
		require.Equal(t, uint64(100), ack.ObservedHeight)
		require.NoError(t, heightsync.VerifyAck(signing.NewSecp256k1Verifier(), ack, ackSigner(session, ack.SlotId)))
	}

	rec := session.HeartbeatTurnTracker().Record(1)
	require.Equal(t, heightsync.TurnComplete, rec.State)
	require.Equal(t, uint64(100), session.HeartbeatTurnTracker().LastCompletedHeight())
	require.True(t, session.HeartbeatTurnTracker().Confirms(100))
}

func TestHeartbeat_UnavailableAcksDoNotCountAndDegrade(t *testing.T) {
	var height uint64 = 100
	session := setupHeartbeatSession(t, &height)
	require.NoError(t, session.MaybeHeartbeat(context.Background()))

	acks := heightAcksInDiffs(session.Diffs())
	require.Len(t, acks, 3, "H24: ack is required even when the oracle is down")
	for _, ack := range acks {
		require.Equal(t, types.SyncState_ORACLE_UNAVAILABLE, ack.SyncState)
	}
	rec := session.HeartbeatTurnTracker().Record(1)
	require.Equal(t, heightsync.TurnOpen, rec.State)
	require.False(t, session.HeartbeatTurnTracker().Confirms(100), "unavailable acks do not confirm (C-turn)")

	height = 102
	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	rec = session.HeartbeatTurnTracker().Record(1)
	require.Equal(t, heightsync.TurnDegraded, rec.State, "H7: < Q counting acks past D_ack")
}

func ackSigner(s *Session, slot uint32) string {
	for _, a := range s.group {
		if a.SlotID == slot {
			return a.ValidatorAddress
		}
	}
	return ""
}

func countHeartbeatForce(d types.Diff) int {
	n := 0
	for _, tx := range d.Txs {
		if f := tx.GetForceHeightSyncTurn(); f != nil && f.Reason == heartbeatForceReason {
			n++
		}
	}
	return n
}
