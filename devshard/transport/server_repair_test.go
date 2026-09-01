package transport

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

type repairPair struct {
	user     *signing.Secp256k1Signer
	hosts    []*signing.Secp256k1Signer
	oracles  []*heightSyncTestOracle
	servers  []*Server
	httpSrv  []*httptest.Server
	hostObjs []*host.Host
}

func setupRepairPair(t *testing.T) *repairPair {
	t.Helper()
	hostSigners := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(2)
	verifier := signing.NewSecp256k1Verifier()

	pair := &repairPair{user: user, hosts: hostSigners, oracles: make([]*heightSyncTestOracle, 2)}
	for i := 0; i < 2; i++ {
		or := &heightSyncTestOracle{hdr: &blocks.Header{
			Height: 500, ChainID: "c", BlockHash: []byte{0xaa, byte(i)},
		}}
		pair.oracles[i] = or
		sm, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier,
			testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000))
		require.NoError(t, err)
		store := storage.NewMemory()
		require.NoError(t, store.CreateSession(storage.CreateSessionParams{
			EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion,
			Config: config, Group: group, InitialBalance: 100000,
		}))
		h, err := host.NewHost(sm, hostSigners[i], stub.NewInferenceEngine(), "escrow-1", group, nil,
			host.WithGrace(100),
			host.WithStorage(store),
			host.WithChainOracle(or),
			host.WithRepairConfig(heightsync.RepairConfig{Stagger: 0}),
		)
		require.NoError(t, err)
		srv, err := NewServer(h, store, verifier, user.Address())
		require.NoError(t, err)
		e := echo.New()
		registerServer(e.Group(testRoutePrefix), srv)
		ts := httptest.NewServer(e)
		t.Cleanup(ts.Close)
		pair.servers = append(pair.servers, srv)
		pair.httpSrv = append(pair.httpSrv, ts)
		pair.hostObjs = append(pair.hostObjs, h)
	}
	return pair
}

func (p *repairPair) applyHeartbeatSpan(t *testing.T) {
	t.Helper()
	hash := []byte{0xaa}
	d1 := testutil.SignDiff(t, p.user, "escrow-1", 1, []*types.DevshardTx{{
		Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			TurnSeq: 1, ObservedHeight: 500, ObservedBlockHash: hash, SlotsNum: 2,
			Reason: string(heightsync.ReasonQuietSession),
		}},
	}})
	d2 := testutil.SignDiff(t, p.user, "escrow-1", 2, []*types.DevshardTx{{
		Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			TurnSeq: 1, ObservedHeight: 500, ObservedBlockHash: hash, SlotsNum: 2,
			Reason: string(heightsync.ReasonQuietSession),
		}},
	}})
	ctx := context.Background()
	for _, h := range p.hostObjs {
		_, err := h.HandleRequest(ctx, host.HostRequest{Diffs: []types.Diff{d1, d2}})
		require.NoError(t, err)
	}
}

// applyWindowClosedStamp lands a host-signed height past D_ack so the
// tracker clock — not the local oracle or a user heartbeat — closes the
// turn's ack window.
func (p *repairPair) applyWindowClosedStamp(t *testing.T) {
	t.Helper()
	past := uint64(pastAckWindow())
	ack := &types.MsgHeightAck{
		TurnSeq: 1, RefNonce: 1, SlotId: 0,
		ObservedHeight: past, ObservedBlockHash: []byte{0xaa},
		SyncState: types.SyncState_SYNCED, PeerSeen: []byte{0xff},
	}
	require.NoError(t, heightsync.SignAck(p.hosts[0], ack))
	d3 := testutil.SignDiff(t, p.user, "escrow-1", 3, []*types.DevshardTx{
		{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}},
	})
	ctx := context.Background()
	for _, h := range p.hostObjs {
		_, err := h.HandleRequest(ctx, host.HostRequest{Diffs: []types.Diff{d3}})
		require.NoError(t, err)
	}
}

// pastAckWindow is the first height at which the turn requested at 500 is
// overdue: repair probes wait out the producer's whole turnover budget, so a
// couple of blocks past h_req is still inside the window.
func pastAckWindow() int64 {
	return 500 + int64(heightsync.DefaultHeartbeatConfig().AckDeadlineBlocks) + 1
}

func (p *repairPair) setOracle(i int, height int64, hash []byte) {
	p.oracles[i].hdr = &blocks.Header{Height: height, ChainID: "c", BlockHash: append([]byte(nil), hash...)}
}

func (p *repairPair) wirePeersFrom(prober int) {
	peers := make(map[int]*HTTPClient)
	for j, ts := range p.httpSrv {
		peers[j] = NewHTTPClient(ts.URL, "escrow-1", p.user, ClientConfig{
			QueryTimeout: DefaultRepairTimeout,
			RoutePrefix:  testRoutePrefix,
		})
	}
	p.servers[prober].SetPeerClients(peers)
}

func assertNoRepairBlame(t *testing.T, h *host.Host, srv *Server) {
	t.Helper()
	for _, m := range h.HeightSyncMarks() {
		require.NotContains(t, strings.ToLower(m.Detail), "user_cheating")
		require.NotContains(t, string(m.Kind), "USER_CHEATING")
	}
	require.Empty(t, h.HeightSyncMarks())
	if ml := srv.HeightSyncMarks(); ml != nil {
		require.Empty(t, ml.All())
	}
}

func TestRepairProbe_UnreachableOrHeight(t *testing.T) {
	p := setupRepairPair(t)
	p.applyHeartbeatSpan(t)
	p.applyWindowClosedStamp(t)

	p.setOracle(0, pastAckWindow(), []byte{0xaa})
	p.setOracle(1, 510, []byte{0xbb})
	p.wirePeersFrom(0)

	p.hostObjs[0].MaybeRepair(context.Background())

	h0 := p.hostObjs[0]
	rec := h0.HeightSyncTurnRecord(1)
	require.NotNil(t, rec)
	require.Equal(t, heightsync.TurnDegraded, rec.State, "probe must not complete the turn")
	require.True(t, h0.PeerSeenHas(1), "peer_seen bit for live peer")
	require.Equal(t, uint64(510), h0.PeerSeenHeight(1))
	require.Equal(t, 1, h0.RepairBudget().Count(heightsync.RepairOutcomeHeight))
	require.Zero(t, h0.RepairBudget().Count(heightsync.RepairOutcomeUnreachable))
	assertNoRepairBlame(t, h0, p.servers[0])

	var courtesy bool
	for _, tx := range h0.MempoolTxs() {
		if ack := tx.GetHeightAck(); ack != nil && ack.SlotId == 1 {
			courtesy = true
			require.Equal(t, uint64(510), ack.ObservedHeight)
		}
	}
	require.True(t, courtesy, "HEIGHT may place the offered ack in the local mempool")
}

func TestRepairProbe_DeadPeerBacksOff(t *testing.T) {
	p := setupRepairPair(t)
	p.applyHeartbeatSpan(t)
	p.applyWindowClosedStamp(t)
	p.setOracle(0, pastAckWindow(), []byte{0xaa})

	p.httpSrv[1].Close()
	p.wirePeersFrom(0)

	now := time.Unix(1_700_000_000, 0)
	p.hostObjs[0].RepairBudget().SetClock(func() time.Time { return now }, func(time.Duration) {})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	p.hostObjs[0].MaybeRepair(ctx)

	h0 := p.hostObjs[0]
	require.Equal(t, 1, h0.RepairBudget().Count(heightsync.RepairOutcomeUnreachable))
	require.Zero(t, h0.RepairBudget().Count(heightsync.RepairOutcomeHeight))
	require.Equal(t, 1, h0.RepairBudget().FailCount(1))
	require.True(t, h0.RepairBudget().InBackoff(1))
	require.Equal(t, uint64(0), h0.PeerSeenHeight(1), "UNREACHABLE does not ingest a probe height")
	require.Equal(t, heightsync.TurnDegraded, h0.HeightSyncTurnRecord(1).State)
	assertNoRepairBlame(t, h0, p.servers[0])
	require.Nil(t, p.servers[0].gossip, "nothing on the wire toward the user")
}

func TestRepairProbe_OracleAheadDoesNotDegradeOpenTurn(t *testing.T) {
	// Two hosts apply the same diffs; A's oracle is past D_ack. Repair
	// must not AdvanceHeight with that tip — both trackers stay TurnOpen.
	p := setupRepairPair(t)
	p.applyHeartbeatSpan(t)

	p.setOracle(0, pastAckWindow(), []byte{0xaa})
	p.setOracle(1, 500, []byte{0xbb})
	p.wirePeersFrom(0)

	p.hostObjs[0].MaybeRepair(context.Background())

	for i, h := range p.hostObjs {
		rec := h.HeightSyncTurnRecord(1)
		require.NotNil(t, rec, "host %d", i)
		require.Equal(t, heightsync.TurnOpen, rec.State, "host %d", i)
	}
	require.Zero(t, p.hostObjs[0].RepairBudget().Count(heightsync.RepairOutcomeHeight))
	require.Zero(t, p.hostObjs[0].RepairBudget().Count(heightsync.RepairOutcomeUnreachable))
	assertNoRepairBlame(t, p.hostObjs[0], p.servers[0])
}

func TestHandleHeightSyncRepair_RejectsUser(t *testing.T) {
	p := setupRepairPair(t)
	req := heightsync.RepairRequest{TurnSeq: 1, RequesterSlot: 0}
	body, err := json.Marshal(req)
	require.NoError(t, err)

	ts := time.Now().Unix()
	sig, err := SignRequest(p.user, "escrow-1", body, ts)
	require.NoError(t, err)
	httpReq := httptest.NewRequest(http.MethodPost,
		testRoutePrefix+"/sessions/escrow-1/heightsync/repair",
		strings.NewReader(string(body)))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	httpReq.Header.Set(HeaderTimestamp, fmt.Sprintf("%d", ts))
	rec := httptest.NewRecorder()
	p.httpSrv[0].Config.Handler.ServeHTTP(rec, httpReq)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandleHeightSyncRepair_RejectsUnsignedDomain(t *testing.T) {
	p := setupRepairPair(t)
	req := &heightsync.RepairRequest{
		TurnSeq: 1, RefNonce: 1, RequesterSlot: 0,
		ObservedHeight: 500, ObservedBlockHash: []byte{0xaa},
	}
	body, err := json.Marshal(req)
	require.NoError(t, err)
	ts := time.Now().Unix()
	sig, err := SignRequest(p.hosts[0], "escrow-1", body, ts)
	require.NoError(t, err)
	httpReq := httptest.NewRequest(http.MethodPost,
		testRoutePrefix+"/sessions/escrow-1/heightsync/repair",
		strings.NewReader(string(body)))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	httpReq.Header.Set(HeaderTimestamp, fmt.Sprintf("%d", ts))
	rec := httptest.NewRecorder()
	p.httpSrv[0].Config.Handler.ServeHTTP(rec, httpReq)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func (p *repairPair) postSignedRepair(t *testing.T, from, to int, req *heightsync.RepairRequest) *httptest.ResponseRecorder {
	t.Helper()
	require.NoError(t, heightsync.SignRepairRequest(p.hosts[from], req))
	body, err := json.Marshal(req)
	require.NoError(t, err)
	ts := time.Now().Unix()
	sig, err := SignRequest(p.hosts[from], "escrow-1", body, ts)
	require.NoError(t, err)
	httpReq := httptest.NewRequest(http.MethodPost,
		testRoutePrefix+"/sessions/escrow-1/heightsync/repair",
		strings.NewReader(string(body)))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	httpReq.Header.Set(HeaderTimestamp, fmt.Sprintf("%d", ts))
	rec := httptest.NewRecorder()
	p.httpSrv[to].Config.Handler.ServeHTTP(rec, httpReq)
	return rec
}

func TestHandleHeightSyncRepair_UnknownTurnSkipsOracle(t *testing.T) {
	p := setupRepairPair(t)
	req := &heightsync.RepairRequest{
		TurnSeq: 1, RefNonce: 1, RequesterSlot: 0,
		ObservedHeight: 500, ObservedBlockHash: []byte{0xaa},
	}
	rec := p.postSignedRepair(t, 0, 1, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Zero(t, p.oracles[1].LatestCalls())
	assertNoRepairBlame(t, p.hostObjs[1], p.servers[1])
}

func TestHandleHeightSyncRepair_FloodBoundsOracleReads(t *testing.T) {
	p := setupRepairPair(t)
	p.applyHeartbeatSpan(t)
	require.NotNil(t, p.hostObjs[1].HeightSyncTurnRecord(1))
	before := p.oracles[1].LatestCalls()

	req := &heightsync.RepairRequest{
		TurnSeq: 1, RefNonce: 1, RequesterSlot: 0,
		ObservedHeight: 500, ObservedBlockHash: []byte{0xaa},
	}
	first := p.postSignedRepair(t, 0, 1, req)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	for i := 0; i < 19; i++ {
		rec := p.postSignedRepair(t, 0, 1, req)
		require.Equal(t, http.StatusTooManyRequests, rec.Code)
	}
	require.Equal(t, before+1, p.oracles[1].LatestCalls(), "one HEIGHT build per (turn, requester)")
	assertNoRepairBlame(t, p.hostObjs[1], p.servers[1])
}

func TestRepairProbe_DegradedOlderTurnStillProbed(t *testing.T) {
	p := setupRepairPair(t)
	p.applyHeartbeatSpan(t)
	p.applyWindowClosedStamp(t)

	hash := []byte{0xaa}
	d4 := testutil.SignDiff(t, p.user, "escrow-1", 4, []*types.DevshardTx{{
		Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			TurnSeq: 2, ObservedHeight: 500, ObservedBlockHash: hash, SlotsNum: 2,
			Reason: string(heightsync.ReasonQuietSession),
		}},
	}})
	d5 := testutil.SignDiff(t, p.user, "escrow-1", 5, []*types.DevshardTx{{
		Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			TurnSeq: 2, ObservedHeight: 500, ObservedBlockHash: hash, SlotsNum: 2,
			Reason: string(heightsync.ReasonQuietSession),
		}},
	}})
	ctx := context.Background()
	for _, h := range p.hostObjs {
		_, err := h.HandleRequest(ctx, host.HostRequest{Diffs: []types.Diff{d4, d5}})
		require.NoError(t, err)
	}

	p.setOracle(0, pastAckWindow(), []byte{0xaa})
	var probed []uint64
	p.hostObjs[0].SetRepairProbe(func(_ context.Context, _ uint32, req *heightsync.RepairRequest) (*heightsync.RepairResponse, error) {
		probed = append(probed, req.TurnSeq)
		return &heightsync.RepairResponse{
			Outcome:           heightsync.RepairOutcomeHeight,
			ObservedHeight:    510,
			ObservedBlockHash: []byte{0xbb},
		}, nil
	})
	p.hostObjs[0].MaybeRepair(ctx)
	require.Contains(t, probed, uint64(1), "turn 1 must still be probed after turn 2 opened")
}

func TestRepairProbe_CancelInterruptsSleep(t *testing.T) {
	p := setupRepairPair(t)
	p.applyHeartbeatSpan(t)
	p.applyWindowClosedStamp(t)
	p.setOracle(0, pastAckWindow(), []byte{0xaa})
	p.wirePeersFrom(0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	p.hostObjs[0].MaybeRepair(ctx)
	require.Less(t, time.Since(start), 200*time.Millisecond)
	require.Zero(t, p.hostObjs[0].RepairBudget().Count(heightsync.RepairOutcomeHeight))
	require.Zero(t, p.hostObjs[0].RepairBudget().Count(heightsync.RepairOutcomeUnreachable))
}
