package user

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commrc "common/runtimeconfig"

	"common/chainoracle/blocks"
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
	blind  atomic.Bool
}

func (o *sessionOracle) Latest(context.Context) (*blocks.Header, error) {
	if o.err != nil {
		return nil, o.err
	}
	if o.blind.Load() {
		return nil, errors.New("follower unavailable")
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
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
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

func TestUser_ForceHeightSyncTurn_SlotsNumFollowsGroupNotCadenceOverride(t *testing.T) {
	const numHosts = 3
	session, _, _ := setupSessionWithOptions(t, numHosts, 100000, 100, WithHeightSyncCadence(10, 1))
	ctx := context.Background()
	_, err := session.SendInference(ctx, InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
		ForceHeightSyncAnchor: true,
	})
	require.NoError(t, err)
	var force *types.MsgForceHeightSyncTurn
	for _, tx := range session.Diffs()[0].Txs {
		if inner := tx.GetForceHeightSyncTurn(); inner != nil {
			force = inner
			break
		}
	}
	require.NotNil(t, force)
	require.Equal(t, uint64(numHosts), force.SlotsNum,
		"scheduler default slots=1 must not be copied onto MsgForceHeightSyncTurn")
	require.Equal(t, uint64(numHosts), force.EndNonce)
}

// setupHeartbeatSession is the blind-roster fixture: the followers seed F on the
// bootstrap inference and then go away, so acks are ORACLE_UNAVAILABLE while the
// escrow still has logical time.
//
// Blindness has to be arrived at rather than started from. Since §10.3.1 a
// heartbeat may not open until a host-signed stamp has set F, so a roster that
// could never read a height has no floor, no truthful observed_height, and
// therefore no cadence at all — see TestHeartbeat_NoFloorSkipsUntilFirstInference.
// A follower that dies mid-session is also the failure that actually happens.
func setupHeartbeatSession(t *testing.T, height *uint64) *Session {
	t.Helper()
	return setupBlindHeartbeatSession(t, height)
}

func setupBlindHeartbeatSession(t *testing.T, height *uint64, extra ...SessionOption) *Session {
	t.Helper()
	oracles, followers := sessionOracles(3, *height)
	session := setupHeartbeatSessionWithOracles(t, height, oracles, extra...)
	for _, o := range followers {
		o.blind.Store(true)
	}
	return session
}

func sessionOracles(n int, height uint64) ([]blocks.BlockOracle, []*sessionOracle) {
	oracles := make([]blocks.BlockOracle, n)
	own := make([]*sessionOracle, n)
	for i := range oracles {
		o := &sessionOracle{hash: []byte{0xaa}}
		o.height.Store(int64(height))
		oracles[i], own[i] = o, o
	}
	return oracles, own
}

// setupHeartbeatSessionWithOracles builds the roster and runs the one inference
// §10.3.1 requires before the cadence can start: F is seeded by a host stamp, and
// only then does MsgHeartbeat have a height it may carry.
func setupHeartbeatSessionWithOracles(t *testing.T, height *uint64, oracles []blocks.BlockOracle, extra ...SessionOption) *Session {
	t.Helper()
	session := setupFloorlessHeartbeatSession(t, height, oracles, extra...)
	seedFloorByInference(t, session)
	return session
}

// seedFloorByInference is the §10.3.1 bootstrap, and it is also the shape of it:
// the start lands hashless, because F does not exist yet, and the executor's
// host-signed confirm/finish rides the next diff and sets F. Hence the drain —
// the receipts sit in pendingTxs until some diff carries them into the log.
//
// One inference credits one slot, and Q is two, so this is deliberately not a
// turnover: every fixture built on it still owes a heartbeat immediately.
func seedFloorByInference(t *testing.T, session *Session) {
	t.Helper()
	_, err := session.SendInference(context.Background(), InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	require.NoError(t, session.SendPendingDiff(context.Background()))

	floor, _, known := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.True(t, known)
	require.NotZero(t, floor, "the bootstrap inference must leave a host-seeded floor")
	require.Zero(t, session.HeartbeatTurnovers(), "one slot is not Q: the cadence is still owed a turn")
}

// setupFloorlessHeartbeatSession is the session before its first inference: no
// host stamp has landed, so F does not exist. Only fixtures about that state, or
// ones that drive the scheduler directly, want it.
func setupFloorlessHeartbeatSession(t *testing.T, height *uint64, oracles []blocks.BlockOracle, extra ...SessionOption) *Session {
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
	opts := []SessionOption{
		WithHeightSyncCadence(10, uint64(numHosts)),
		WithObservedHeight(func() (uint64, []byte, bool) {
			h := *height
			if h == 0 {
				return 0, nil, false
			}
			return h, []byte{0xaa}, true
		}),
	}
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier,
		append(opts, extra...)...)
	require.NoError(t, err)
	return session
}

// heartbeatDiffsAfter is the span dispatched after nonce base, in nonce order.
// A turn is named by the nonce its span lands at, and since §10.3.1 that is never
// nonce 1: the bootstrap inference goes first.
func heartbeatDiffsAfter(diffs []types.Diff, base uint64) []types.Diff {
	var out []types.Diff
	for _, d := range diffs {
		if d.Nonce <= base || countHeartbeats([]types.Diff{d}) == 0 {
			continue
		}
		out = append(out, d)
	}
	return out
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
	base := session.Nonce()
	ctx := context.Background()

	require.NoError(t, session.MaybeHeartbeat(ctx))
	span := heartbeatDiffsAfter(session.Diffs(), base)
	require.GreaterOrEqual(t, len(span), 3, "slots_num heartbeat diffs")
	force := span[0].Txs[0].GetForceHeightSyncTurn()
	require.NotNil(t, force)
	require.Equal(t, base+1, force.TriggerNonce)
	require.Equal(t, "heartbeat", force.Reason)
	hb := span[0].Txs[1].GetHeartbeat()
	require.NotNil(t, hb)
	require.Equal(t, uint64(100), hb.ObservedHeight,
		"the stamp is F, which the bootstrap inference seeded at the host's tip")

	rec := session.HeartbeatTurnTracker().Record(base + 1)
	require.NotNil(t, rec)
	require.Equal(t, base+1, rec.TurnStart)
	require.Equal(t, uint64(100), rec.HReq)
}

func TestHeartbeat_ForceSlotsNumFollowsGroupNotCadenceOverride(t *testing.T) {
	var height uint64 = 100
	session := setupHeartbeatSession(t, &height)
	base := session.Nonce()
	session.SetHeightSyncCadence(10, 1)
	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	span := heartbeatDiffsAfter(session.Diffs(), base)
	require.NotEmpty(t, span)
	force := span[0].Txs[0].GetForceHeightSyncTurn()
	require.NotNil(t, force)
	require.Equal(t, uint64(3), force.SlotsNum,
		"scheduler default slots=1 must not be copied onto MsgForceHeightSyncTurn")
	require.Equal(t, base+3, force.EndNonce)
}

// TestHeartbeat_NoFloorSkipsUntilFirstInference is §10.3.1 on the producer side.
//
// The courier tip is available here — the fixture hands the session a height — and
// it still may not heartbeat, because that tip is not a height the user read from
// any chain. Only a host-signed stamp can seed F, and F is the only thing a
// heartbeat may carry. So the loop stays disarmed until one inference has run,
// which is also what closes P1: there is no user-chosen integer left to write.
func TestHeartbeat_NoFloorSkipsUntilFirstInference(t *testing.T) {
	var height uint64 = 100
	oracles, _ := sessionOracles(3, height)
	session := setupFloorlessHeartbeatSession(t, &height, oracles)

	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	require.Empty(t, session.Diffs(), "no floor: nothing truthful to stamp, so no turn opens")
	require.Equal(t, 1, session.HeartbeatSkippedNoHeight())
	require.Equal(t, uint64(0), session.Nonce())

	seedFloorByInference(t, session)
	base := session.Nonce()
	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	span := heartbeatDiffsAfter(session.Diffs(), base)
	require.NotEmpty(t, span, "the first host stamp arms the cadence")
	require.Equal(t, uint64(100), span[0].Txs[1].GetHeartbeat().ObservedHeight)
}

// TestHeartbeat_NoHeightAnywhereSkips: a session whose hosts have no follower at
// all never acquires a floor, so it never heartbeats. Hosts see silence and arm
// close-ready on T_idle, which is the same path as a session that never spoke.
func TestHeartbeat_NoHeightAnywhereSkips(t *testing.T) {
	var height uint64
	oracles, followers := sessionOracles(3, 0)
	for _, o := range followers {
		o.blind.Store(true)
	}
	session := setupFloorlessHeartbeatSession(t, &height, oracles)
	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	require.Empty(t, session.Diffs())
	require.Equal(t, 1, session.HeartbeatSkippedNoHeight())
	require.Equal(t, uint64(0), session.Nonce())
}

func TestHeartbeat_SpanDispatchAddressesEverySlot(t *testing.T) {
	var height uint64 = 100
	session := setupHeartbeatSession(t, &height)
	base := session.Nonce()
	require.NoError(t, session.MaybeHeartbeat(context.Background()))

	diffs := session.Diffs()
	const slots = 3
	span := heartbeatDiffsAfter(diffs, base)
	require.GreaterOrEqual(t, len(span), slots)
	span = span[:slots]
	seen := map[uint32]int{}
	for i, d := range span {
		require.Equal(t, base+uint64(i)+1, d.Nonce)
		var hb *types.MsgHeartbeat
		for _, tx := range d.Txs {
			if inner := tx.GetHeartbeat(); inner != nil {
				hb = inner
			}
			require.Nil(t, tx.GetHeightAck(), "span must not wait for acks")
		}
		require.NotNil(t, hb, "diff %d missing MsgHeartbeat", d.Nonce)
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
	// The span awaited no ack, so one ack-carrying nonce is always owed after it.
	require.Greater(t, session.Nonce(), base+slots)
}

func TestHeartbeat_AckInclusionAndSyncVectorPrevTurn(t *testing.T) {
	var height uint64 = 100
	now := time.Unix(1000, 0).UTC()
	session := setupBlindHeartbeatSession(t, &height,
		WithHeartbeatClock(func() time.Time { return now }))
	base := session.Nonce()
	ctx := context.Background()
	require.NoError(t, session.MaybeHeartbeat(ctx))

	diffs := session.Diffs()
	const slots = 3
	span := heartbeatDiffsAfter(diffs, base)
	require.GreaterOrEqual(t, len(span), slots)
	for _, d := range span[:slots] {
		for _, tx := range d.Txs {
			require.Nil(t, tx.GetHeightAck(), "span must not wait for acks")
		}
	}
	acks := heightAcksInDiffs(diffs)
	require.Len(t, acks, slots, "flush round must include one host ack per slot")
	seen := map[uint32]types.SyncState{}
	for _, ack := range acks {
		require.Greater(t, ack.RefNonce, base)
		require.LessOrEqual(t, ack.RefNonce, base+slots,
			"each ack answers the heartbeat of its own slot inside the span")
		require.Equal(t, types.SyncState_ORACLE_UNAVAILABLE, ack.SyncState)
		require.Equal(t, uint64(100), ack.ObservedHeight,
			"the follower is gone, so the ack carries F rather than a fresh reading")
		seen[ack.SlotId] = ack.SyncState
	}
	require.Len(t, seen, slots)

	rec := session.HeartbeatTurnTracker().Record(base + 1)
	require.Equal(t, heightsync.TurnComplete, rec.State,
		"the turn certifies reachability, which these acks prove")

	// The cadence is satisfied, so the next turn is owed only after Interval.
	height = 102
	now = now.Add(heightsync.DefaultHeartbeatConfig().Interval + time.Second)
	require.NoError(t, session.MaybeHeartbeat(ctx))

	// The second turn is the newest one in the log; heartbeats no longer name a
	// turn, so the latest diff carrying one is the selector.
	var hb *types.MsgHeartbeat
	var hbNonce uint64
	for _, d := range session.Diffs() {
		for _, tx := range d.Txs {
			if inner := tx.GetHeartbeat(); inner != nil && d.Nonce >= hbNonce {
				hb, hbNonce = inner, d.Nonce
			}
		}
	}
	require.Greater(t, hbNonce, base+slots, "second turn's heartbeat lands after the first")
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
	base := session.Nonce()
	require.NoError(t, session.MaybeHeartbeat(context.Background()))

	acks := heightAcksInDiffs(session.Diffs())
	require.Len(t, acks, 3)
	for _, ack := range acks {
		require.Equal(t, types.SyncState_SYNCED, ack.SyncState)
		require.Equal(t, uint64(100), ack.ObservedHeight)
		require.NoError(t, heightsync.VerifyAck(signing.NewSecp256k1Verifier(), ack, ackSigner(session, ack.SlotId)))
	}

	rec := session.HeartbeatTurnTracker().Record(base + 1)
	require.Equal(t, heightsync.TurnComplete, rec.State)
	require.Equal(t, uint64(100), session.HeartbeatTurnTracker().LastCompletedHeight())
	require.True(t, session.HeartbeatTurnTracker().CompletedAtOrAbove(100))
}

// TestHeartbeat_CarriesTheFloorNotTheCourierTip is the producer half of §10.3.1.
//
// The user's "own view" is a bag of peer tips it collected from response
// envelopes; it read no chain itself. Writing that into Diff is what P1 was
// about — a user-chosen integer sitting where the log keeps logical time — so the
// only stamp a heartbeat may carry is F, at any distance, and the courier tip
// stays on the request envelope where the receiving host judges it against its
// own follower.
func TestHeartbeat_CarriesTheFloorNotTheCourierTip(t *testing.T) {
	var height uint64 = 80
	now := time.Unix(1000, 0).UTC()
	oracles := make([]blocks.BlockOracle, 3)
	for i := range oracles {
		oracles[i] = &sessionOracle{hash: []byte{0xaa}}
		oracles[i].(*sessionOracle).height.Store(80)
	}
	session := setupHeartbeatSessionWithOracles(t, &height, oracles,
		WithHeartbeatClock(func() time.Time { return now }))
	base := session.Nonce()
	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	floor, _, known := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.True(t, known)
	require.Equal(t, uint64(80), floor)

	height = 5_000 // courier tip runs ahead; the hosts, and so F, stay at 80
	now = now.Add(heightsync.DefaultHeartbeatConfig().Interval + time.Second)
	require.NoError(t, session.MaybeHeartbeat(context.Background()))

	var hb *types.MsgHeartbeat
	var hbNonce uint64
	for _, d := range session.Diffs() {
		for _, tx := range d.Txs {
			if inner := tx.GetHeartbeat(); inner != nil && d.Nonce >= hbNonce {
				hb, hbNonce = inner, d.Nonce
			}
		}
	}
	require.NotNil(t, hb)
	require.Greater(t, hbNonce, base+3, "the second turn's heartbeat lands after the first")
	require.Equal(t, uint64(80), hb.ObservedHeight,
		"the heartbeat stamps F; the courier tip is not a height the log will take")
}

// TestHeartbeat_UnavailableAcksCompleteTurnCarryingTheFloor separates the two
// jobs an ack used to do at once. A roster whose followers have died completes
// the turn: the hosts are reachable and applying the log. What they carry is F —
// a height already in the log, signed by whoever put it there — while sync_state
// says plainly that this slot witnessed nothing. So the turn proves reachability
// and not height (TestTurnComplete_IsNotAHeightCertificate), and the carry moves
// no logical time: F stays where the last real witness left it.
func TestHeartbeat_UnavailableAcksCompleteTurnCarryingTheFloor(t *testing.T) {
	var height uint64 = 100
	session := setupHeartbeatSession(t, &height)
	base := session.Nonce()
	require.NoError(t, session.MaybeHeartbeat(context.Background()))

	acks := heightAcksInDiffs(session.Diffs())
	require.Len(t, acks, 3, "ack is required even when the oracle is down")
	for _, ack := range acks {
		require.Equal(t, types.SyncState_ORACLE_UNAVAILABLE, ack.SyncState,
			"the self-report stays honest: this slot is no height witness")
		require.Equal(t, uint64(100), ack.ObservedHeight,
			"with no reading of its own the ack carries F, which is a citation and not a claim")
	}
	floor, _, known := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.True(t, known)
	require.Equal(t, uint64(100), floor, "carries do not raise logical time")
	rec := session.HeartbeatTurnTracker().Record(base + 1)
	require.Equal(t, heightsync.TurnComplete, rec.State)
	for slot, a := range rec.Acks {
		require.Equal(t, types.SyncState_ORACLE_UNAVAILABLE, a.SyncState,
			"slot %d is on record as unable to witness the height it carried", slot)
	}
}

func TestHeartbeat_BusySessionWithStampsEmitsNone(t *testing.T) {
	// Executor-stamped receipts from a quorum of slots are a full height-sync
	// turnover, so the log plane owes no heartbeat.
	var height uint64 = 100
	oracles := make([]blocks.BlockOracle, 3)
	for i := range oracles {
		oracles[i] = &sessionOracle{hash: []byte{0xaa}}
		oracles[i].(*sessionOracle).height.Store(100)
	}
	session := setupHeartbeatSessionWithOracles(t, &height, oracles)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	_, err := session.SendInference(ctx, params)
	require.NoError(t, err)
	_, err = session.SendInference(ctx, params)
	require.NoError(t, err)

	require.NoError(t, session.MaybeHeartbeat(ctx))
	require.Zero(t, countHeartbeats(session.Diffs()), "stamped inference traffic emits zero heartbeats")
	require.Equal(t, uint64(100), session.HeartbeatTurnTracker().LastCompletedHeight())
}

func TestHeartbeat_SustainedInferenceFlowNeverHeartbeats(t *testing.T) {
	// Same rule over time. BusySessionWithStampsEmitsNone only checks one instant, so it
	// would still pass if the cadence never fired at all in this fixture. Here
	// inference keeps flowing while the clock crosses Interval repeatedly: every
	// crossing is a due check that must find a fresh turnover and emit nothing.
	// The tail then stops the traffic and asserts a heartbeat *does* appear, which
	// is what makes the zero above meaningful rather than vacuous.
	var height uint64 = 100
	oracles := make([]blocks.BlockOracle, 3)
	for i := range oracles {
		oracles[i] = &sessionOracle{hash: []byte{0xaa}}
		oracles[i].(*sessionOracle).height.Store(100)
	}
	now := time.Unix(1_700_000_000, 0)
	session := setupHeartbeatSessionWithOracles(t, &height, oracles,
		WithHeartbeatClock(func() time.Time { return now }))
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	// Each round turns the cadence over, then lets almost a full Interval pass
	// before the due check. Over four rounds the clock advances well past several
	// Intervals, so this is a genuinely time-driven flow and not one instant.
	gap := heightsync.DefaultHeartbeatInterval - time.Second
	for round := 0; round < 4; round++ {
		// Q = 2 of 3 slots, so a turnover needs two distinct executors.
		for i := 0; i < 2; i++ {
			_, err := session.SendInference(ctx, params)
			require.NoError(t, err)
		}
		now = now.Add(gap)
		require.NoError(t, session.MaybeHeartbeat(ctx))
		require.Zero(t, countHeartbeats(session.Diffs()),
			"round %d: inference traffic discharges the cadence, so no heartbeat is owed", round)
	}
	require.Greater(t, now.Sub(time.Unix(1_700_000_000, 0)), 2*heightsync.DefaultHeartbeatInterval,
		"sanity: the run must span more than one Interval or it proves nothing about time")
	require.GreaterOrEqual(t, session.HeartbeatTurnovers(), 4,
		"each round's executor stamps must register as a turnover")

	// Traffic stops: the next crossing has nothing to ride and must heartbeat.
	now = now.Add(heightsync.DefaultHeartbeatInterval + time.Second)
	require.NoError(t, session.MaybeHeartbeat(ctx))
	require.NotZero(t, countHeartbeats(session.Diffs()),
		"a silent Interval must still open a turn — otherwise the zeros above prove nothing")
}

// TestHeartbeat_UserOwnStampIsNotATurnover: a stamp the sequencer composed is
// not evidence that anyone answered.
//
// Since §10.3.1 the sequencer's stamp is F itself, so it is no longer a
// user-chosen number — but it is still a number the user wrote alone. Nothing
// about it proves a host is alive, which is the only thing the cadence is for, so
// the obligation stands and a turn still opens. This is the gap the old
// block-based h_last check missed: it saw a height move in the log and called the
// session healthy.
func TestHeartbeat_UserOwnStampIsNotATurnover(t *testing.T) {
	var height uint64 = 100
	session := setupHeartbeatSession(t, &height)
	base := session.Nonce()
	before := session.HeartbeatTurnovers()

	// The sequencer's own start, stamped with F: the highest height in the log,
	// signed by nobody but the user.
	session.observeTurnLocked(types.Diff{Nonce: base + 1, Txs: []*types.DevshardTx{
		{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			InferenceId: base + 1, ObservedHeight: 100, ObservedBlockHash: []byte{0xaa},
		}}},
	}})
	require.Equal(t, before, session.HeartbeatTurnovers(),
		"a self-signed stamp credits no slot")

	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	require.NotEmpty(t, heartbeatDiffsAfter(session.Diffs(), base),
		"a self-signed stamp must not discharge the obligation")
}

func TestHeartbeat_QuietSessionWaitsOutIntervalBetweenTurns(t *testing.T) {
	// The cadence is wall clock: a second turn opens because Interval elapsed,
	// not because a block arrived. Height is held constant throughout.
	var height uint64 = 100
	oracles := make([]blocks.BlockOracle, 3)
	for i := range oracles {
		oracles[i] = &sessionOracle{hash: []byte{0xaa}}
		oracles[i].(*sessionOracle).height.Store(100)
	}
	now := time.Unix(1_700_000_000, 0)
	session := setupHeartbeatSessionWithOracles(t, &height, oracles,
		WithHeartbeatClock(func() time.Time { return now }))
	ctx := context.Background()
	base := session.Nonce()

	require.NoError(t, session.MaybeHeartbeat(ctx))
	require.Equal(t, heightsync.TurnComplete, session.HeartbeatTurnTracker().Record(base+1).State)
	turns := countHeartbeats(session.Diffs())
	require.NotZero(t, turns)

	require.NoError(t, session.MaybeHeartbeat(ctx))
	require.Equal(t, turns, countHeartbeats(session.Diffs()),
		"inside Interval the turnover already discharged the obligation")

	now = now.Add(heightsync.DefaultHeartbeatInterval)
	require.NoError(t, session.MaybeHeartbeat(ctx))
	require.Greater(t, countHeartbeats(session.Diffs()), turns,
		"Interval elapsed at an unchanged height still opens a turn")
	require.NotNil(t, session.HeartbeatTurnTracker().Latest())
}

func TestHeartbeat_OverlayShortensCadence(t *testing.T) {
	cfg := heightsync.HeartbeatConfigFromSnapshot(commrc.Snapshot{
		HeightSync: commrc.HeightSyncParams{
			IntervalMs: 2000, TurnTimeoutMs: 3000, IdleTimeoutMs: 9000,
		},
	})
	require.Equal(t, 2*time.Second, cfg.Interval)

	var height uint64 = 100
	oracles := make([]blocks.BlockOracle, 3)
	for i := range oracles {
		oracles[i] = &sessionOracle{hash: []byte{0xaa}}
		oracles[i].(*sessionOracle).height.Store(100)
	}
	now := time.Unix(1_700_000_000, 0)
	session := setupHeartbeatSessionWithOracles(t, &height, oracles,
		WithHeartbeatConfig(cfg),
		WithHeartbeatClock(func() time.Time { return now }))
	ctx := context.Background()
	base := session.Nonce()

	require.NoError(t, session.MaybeHeartbeat(ctx))
	require.Equal(t, heightsync.TurnComplete, session.HeartbeatTurnTracker().Record(base+1).State)
	turns := countHeartbeats(session.Diffs())

	now = now.Add(2 * time.Second)
	require.NoError(t, session.MaybeHeartbeat(ctx))
	require.Greater(t, countHeartbeats(session.Diffs()), turns,
		"overlay Interval=2s opens the next turn before the compiled 6s")
	require.NotNil(t, session.HeartbeatTurnTracker().Latest())
}

func countHeartbeats(diffs []types.Diff) int {
	n := 0
	for _, d := range diffs {
		for _, tx := range d.Txs {
			if tx.GetHeartbeat() != nil {
				n++
			}
		}
	}
	return n
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

func TestHeartbeat_LoopOpensQuietTurnWithoutCaller(t *testing.T) {
	var height uint64 = 100
	session := setupBlindHeartbeatSession(t, &height,
		WithHeartbeatConfig(heightsync.HeartbeatConfig{Interval: 40 * time.Millisecond}))
	t.Cleanup(func() { _ = session.Close() })
	base := session.Nonce()

	session.StartHeartbeatLoop()
	require.Eventually(t, func() bool {
		return session.Nonce() >= base+3 && session.HeartbeatTurnTracker().Record(base+1) != nil
	}, 2*time.Second, 10*time.Millisecond, "loop must open a turn without the test calling MaybeHeartbeat")

	require.GreaterOrEqual(t, countHeartbeats(session.Diffs()), 3)
	require.Equal(t, base+1, session.StateMachine().HeightSyncLatestTurnStart())
}

func TestHeartbeat_SpanDispatchConcurrentAndContinuesOnError(t *testing.T) {
	var height uint64 = 100
	session := setupHeartbeatSession(t, &height)
	t.Cleanup(func() { _ = session.Close() })
	base := session.Nonce()

	release := make(chan struct{})
	started := make(chan struct{}, 3)
	var calls [3]atomic.Int32
	clients := session.Clients()
	inners := make([]*InProcessClient, len(clients))
	for i, c := range clients {
		inner, ok := c.(*InProcessClient)
		require.True(t, ok, "host %d", i)
		inners[i] = inner
		p := &spanProbeClient{inner: inner, started: started, calls: &calls[i]}
		if i == 0 {
			p.block = release
		}
		if i == 1 {
			p.fail = errors.New("injected span send failure")
		}
		clients[i] = p
	}

	done := make(chan error, 1)
	go func() { done <- session.MaybeHeartbeat(context.Background()) }()

	deadline := time.After(2 * time.Second)
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-deadline:
			t.Fatalf("only %d of 3 span sends started; dispatch is waiting for slot 0", i)
		}
	}
	close(release)

	require.NoError(t, <-done)
	require.GreaterOrEqual(t, calls[0].Load(), int32(1))
	require.GreaterOrEqual(t, calls[1].Load(), int32(1))
	require.GreaterOrEqual(t, calls[2].Load(), int32(1))
	require.GreaterOrEqual(t, inners[0].Host.LatestNonce(), base+1, "slot 0 still received its heartbeat")
	require.Less(t, inners[1].Host.LatestNonce(), base+1, "failed send must not reach slot 1 with the span")
	require.GreaterOrEqual(t, inners[2].Host.LatestNonce(), base+3, "slot 2 still received its heartbeat")
}

func TestHeartbeat_LoopStopsOnClose(t *testing.T) {
	var height uint64 = 100
	interval := 40 * time.Millisecond
	session := setupBlindHeartbeatSession(t, &height,
		WithHeartbeatConfig(heightsync.HeartbeatConfig{Interval: interval}))
	base := session.Nonce()

	session.StartHeartbeatLoop()
	require.Eventually(t, func() bool { return session.Nonce() >= base+3 }, 2*time.Second, 10*time.Millisecond)
	require.NoError(t, session.Close())

	nonce := session.Nonce()
	time.Sleep(5 * interval)
	require.Equal(t, nonce, session.Nonce(), "Close must cancel the ticker")
}

// The producer's turn state is a function of the log, not of the clock or of the
// user's own view. Here the hosts are silenced, so no ack can complete the turn,
// and the courier tip then runs past D_ack — which used to be the thing that
// stamped the next span and is now no height the log will take at all (§10.3.1).
// The turn must stay exactly as the SM holds it: Open, at the same turn_start.
func TestHeartbeat_SettleTurnDoesNotFireWhileSMTurnOpen(t *testing.T) {
	var height uint64 = 100
	session := setupHeartbeatSession(t, &height)
	t.Cleanup(func() { _ = session.Close() })
	base := session.Nonce()

	clients := session.Clients()
	started := make(chan struct{}, 8)
	fail := errors.New("no host ack")
	for i, c := range clients {
		inner, ok := c.(*InProcessClient)
		require.True(t, ok, "host %d", i)
		clients[i] = &spanProbeClient{inner: inner, started: started, calls: new(atomic.Int32), fail: fail}
	}

	require.NoError(t, session.MaybeHeartbeat(context.Background()))
	turn := base + 1
	sessRec := session.HeartbeatTurnTracker().Record(turn)
	smRec := session.StateMachine().HeightSyncTurnRecord(turn)
	require.NotNil(t, sessRec)
	require.NotNil(t, smRec)
	require.Equal(t, heightsync.TurnOpen, sessRec.State)
	require.Equal(t, heightsync.TurnOpen, smRec.State)
	require.Equal(t, turn, session.HeartbeatTurnTracker().LatestTurnStart())

	height = 100 + heightsync.DefaultHeartbeatConfig().AckDeadlineBlocks + 1
	require.NoError(t, session.MaybeHeartbeat(context.Background()))

	require.Equal(t, heightsync.TurnOpen, session.HeartbeatTurnTracker().Record(turn).State)
	require.Equal(t, heightsync.TurnOpen, session.StateMachine().HeightSyncTurnRecord(turn).State)
	require.Equal(t, turn, session.HeartbeatTurnTracker().LatestTurnStart())
	require.Equal(t, turn, session.StateMachine().HeightSyncLatestTurnStart())
}

type spanProbeClient struct {
	inner   *InProcessClient
	started chan struct{}
	calls   *atomic.Int32
	block   <-chan struct{}
	fail    error
}

func (c *spanProbeClient) Send(ctx context.Context, req host.HostRequest, _ io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	c.calls.Add(1)
	select {
	case c.started <- struct{}{}:
	default:
	}
	if c.block != nil {
		select {
		case <-c.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if c.fail != nil {
		return nil, c.fail
	}
	return c.inner.Send(ctx, req, nil, receiptHandler)
}

func stampedConfirmTx(inferenceID, height uint64) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: inferenceID, ObservedHeight: height, ObservedBlockHash: []byte{0xaa},
	}}}
}

// A confirm whose HTTP response never came back still discharges the cadence
// once it lands in the log through a peer's mempool: the executor signature over
// the stamp is the same round-trip an ack proves.
func TestHeartbeat_LogResidentStampsDischargeCadence(t *testing.T) {
	var height uint64 = 100
	session := setupFloorlessHeartbeatSession(t, &height, nil)
	t.Cleanup(func() { _ = session.Close() })
	require.Equal(t, 2, session.heartbeat.Quorum())

	// Inferences 1 and 2 belong to different executors under id % len(group).
	session.observeTurnLocked(types.Diff{Nonce: 1, Txs: []*types.DevshardTx{
		stampedConfirmTx(1, 100),
		stampedConfirmTx(2, 100),
	}})

	require.Equal(t, 1, session.heartbeat.Turnovers())
	require.True(t, session.heartbeat.LastTurnoverFromStamp(), "Q stamps must turn the cadence over as stamps")
	require.True(t, session.heartbeat.MaybeRecordDischarged(time.Now(), 100))
}

func TestHeartbeat_LogResidentStampsNeedDistinctExecutors(t *testing.T) {
	var height uint64 = 100
	session := setupFloorlessHeartbeatSession(t, &height, nil)
	t.Cleanup(func() { _ = session.Close() })

	// 1 and 4 are the same executor, so this is one claim, not Q.
	session.observeTurnLocked(types.Diff{Nonce: 1, Txs: []*types.DevshardTx{
		stampedConfirmTx(1, 100),
		stampedConfirmTx(4, 100),
	}})
	require.Zero(t, session.heartbeat.Turnovers())

	// A stamped start is the sequencer's own reading and credits nobody.
	session.observeTurnLocked(types.Diff{Nonce: 2, Txs: []*types.DevshardTx{
		{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			InferenceId: 2, ObservedHeight: 100, ObservedBlockHash: []byte{0xaa},
		}}},
	}})
	require.Zero(t, session.heartbeat.Turnovers())

	// A second executor completes Q.
	session.observeTurnLocked(types.Diff{Nonce: 3, Txs: []*types.DevshardTx{stampedConfirmTx(2, 100)}})
	require.Equal(t, 1, session.heartbeat.Turnovers())
}
