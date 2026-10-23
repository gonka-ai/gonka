package transport_test

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport"
	"devshard/transport/rpcserver"
	"devshard/types"
)

type retryRPCLookup struct {
	core retryRPCCore
}

func (l retryRPCLookup) SessionServerExisting(string) (rpcserver.SessionCore, error) {
	return l.core, nil
}
func (l retryRPCLookup) SessionForParticipant(id, addr string) (rpcserver.SessionCore, error) {
	_ = addr
	return l.SessionServerExisting(id)
}
func (l retryRPCLookup) SessionForOwner(id, addr string) (rpcserver.SessionCore, error) {
	_ = addr
	return l.SessionServerExisting(id)
}
func (l retryRPCLookup) SessionForStartProof(id, addr string, _ func() ([]types.Diff, error), _ string) (rpcserver.SessionCore, error) {
	return l.SessionForParticipant(id, addr)
}

type retryRPCCore struct {
	stateHits     *atomic.Int32
	challengeHits *atomic.Int32
	nonce         uint64
	root          []byte
}

func (c retryRPCCore) ServeGetSignatures(uint64) (map[uint32][]byte, error) {
	return map[uint32][]byte{}, nil
}
func (c retryRPCCore) AllowsSender(string) bool  { return true }
func (c retryRPCCore) IsGroupMember(string) bool { return true }
func (c retryRPCCore) ServeGetState() (uint64, []byte, error) {
	if c.stateHits != nil {
		c.stateHits.Add(1)
	}
	return c.nonce, append([]byte(nil), c.root...), nil
}
func (c retryRPCCore) ServeChallengeReceipt(context.Context, transport.ChallengeReceiptRequest) (*transport.ChallengeReceiptResponse, error) {
	if c.challengeHits != nil {
		c.challengeHits.Add(1)
	}
	return &transport.ChallengeReceiptResponse{}, nil
}

func TestRefusalRetryUsesThePeerRPC(t *testing.T) {
	root := bytes.Repeat([]byte{7}, 32)
	var stateHits, challengeHits atomic.Int32
	rpc := newLargeRPCClient(t, retryRPCLookup{core: retryRPCCore{
		stateHits: &stateHits, challengeHits: &challengeHits, nonce: 4, root: root,
	}}, transport.EndpointState+","+transport.EndpointChallengeReceipt, transport.DefaultClientConfig())
	t.Cleanup(rpc.Close)

	var tip host.SessionTip = rpc
	var exec host.ExecutorClient = rpc
	nonce, got, err := tip.SessionHead(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint64(4), nonce)
	require.Equal(t, root, got)
	_, _, err = exec.ChallengeReceipt(context.Background(), 1, &host.InferencePayload{Prompt: []byte("p")}, nil)
	require.NoError(t, err)
	require.Equal(t, int32(1), stateHits.Load())
	require.Equal(t, int32(1), challengeHits.Load())
}

const refusalJournalDiffs = 30_000

// refusalLargeJournalDiffs is the soak's journal: about 148,000 diffs of
// about 500 bytes, 74 MiB of wire.
const refusalLargeJournalDiffs = 148_000

// largeJournalHeapPages is how far HeapAlloc may sit above the seeded journal
// during one catch-up call. The live set is the stored page plus the body
// this stub marshals to measure it. A decoded journal is on the order of
// 100 MiB and fails the bound.
const largeJournalHeapPages = 4

const refusalJournalEscrow = "escrow-refusal-journal"

// recordedChallenge is one ChallengeReceipt the executor was sent, measured
// the way RPCClient marshals the Connect body.
type recordedChallenge struct {
	size    int
	nonces  []uint64
	payload bool
}

// recordingExecutor stands in for the executor. SessionHead is configured
// per case. ChallengeReceipt records the body and answers without a receipt.
type recordingExecutor struct {
	t            *testing.T
	nonce        uint64
	root         []byte
	headErr      error
	calls        []recordedChallenge
	block        bool
	queryBudget  time.Duration
	verifyBudget time.Duration
	trackHeap    bool
	baseline     uint64
	high         uint64
}

var _ host.ChallengeReceiptWireSender = (*recordingExecutor)(nil)

func (e *recordingExecutor) SessionHead(context.Context) (uint64, []byte, error) {
	return e.nonce, e.root, e.headErr
}

func (e *recordingExecutor) GetMempool(context.Context) ([]*types.DevshardTx, error) {
	return nil, nil
}

func (e *recordingExecutor) RefusalQueryBudget() time.Duration { return e.queryBudget }

func (e *recordingExecutor) RefusalVerifyBudget() time.Duration { return e.verifyBudget }

func (e *recordingExecutor) ChallengeCatchUp(ctx context.Context, inferenceID uint64, diffs []types.DiffWire, _ string) error {
	if err := e.wait(ctx); err != nil {
		return err
	}
	raw, err := transport.ChallengeCatchUpBody(inferenceID, diffs)
	require.NoError(e.t, err)
	e.record(len(raw), diffs, false)
	e.sampleHeap()
	return nil
}

func (e *recordingExecutor) ChallengeReceiptWires(ctx context.Context, inferenceID uint64, payload *host.InferencePayload, diffs []types.DiffWire, _ host.WireReload) ([]byte, []*types.DevshardTx, error) {
	if err := e.wait(ctx); err != nil {
		return nil, nil, err
	}
	raw, err := transport.ChallengeReceiptWireBody(inferenceID, payload, diffs)
	require.NoError(e.t, err)
	e.record(len(raw), diffs, payload != nil)
	e.sampleHeap()
	return nil, nil, nil
}

func (e *recordingExecutor) ChallengeReceipt(ctx context.Context, inferenceID uint64, payload *host.InferencePayload, diffs []types.Diff) ([]byte, []*types.DevshardTx, error) {
	if err := e.wait(ctx); err != nil {
		return nil, nil, err
	}
	raw, err := challengeReceiptBody(inferenceID, payload, diffs)
	require.NoError(e.t, err)
	nonces := make([]uint64, len(diffs))
	for i, d := range diffs {
		nonces[i] = d.Nonce
	}
	e.calls = append(e.calls, recordedChallenge{
		size:    len(raw),
		nonces:  nonces,
		payload: payload != nil,
	})
	e.sampleHeap()
	return nil, nil, nil
}

func (e *recordingExecutor) wait(ctx context.Context) error {
	if !e.block {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func (e *recordingExecutor) record(size int, diffs []types.DiffWire, payload bool) {
	nonces := make([]uint64, len(diffs))
	for i, d := range diffs {
		nonces[i] = d.Nonce
	}
	e.calls = append(e.calls, recordedChallenge{size: size, nonces: nonces, payload: payload})
}

// sampleHeap records retained heap while this call's page is still reachable.
// A GC first drops pages the walk has already finished, so the high-water is
// the live page rather than bytes the collector has not swept.
func (e *recordingExecutor) sampleHeap() {
	if !e.trackHeap {
		return
	}
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if m.HeapAlloc > e.high {
		e.high = m.HeapAlloc
	}
}

func (e *recordingExecutor) HeightSyncRepair(context.Context, *heightsync.RepairRequest) (*heightsync.RepairResponse, error) {
	return nil, errors.New("not used")
}

func (e *recordingExecutor) CloneWithSigner(signing.Signer, time.Duration) transport.HostPeerClient {
	return e
}

func (e *recordingExecutor) Close() {}

func challengeReceiptBody(inferenceID uint64, payload *host.InferencePayload, diffs []types.Diff) ([]byte, error) {
	djList := make([]transport.DiffJSON, len(diffs))
	for i, d := range diffs {
		dj, err := transport.DiffToJSON(d)
		if err != nil {
			return nil, err
		}
		djList[i] = dj
	}
	return proto.Marshal(transport.ChallengeReceiptRequestToProto(transport.ChallengeReceiptRequest{
		InferenceID: inferenceID,
		Payload:     transport.PayloadToJSON(payload),
		Diffs:       djList,
	}))
}

func txsAbout500() []*types.DevshardTx {
	model := strings.Repeat("m", 500)
	var txs []*types.DevshardTx
	for range 8 {
		txs = []*types.DevshardTx{{
			Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{Model: model}},
		}}
		got := proto.Size(&types.DiffContent{Txs: txs})
		if got >= 480 && got <= 520 {
			return txs
		}
		delta := 500 - got
		if delta >= 0 {
			model += strings.Repeat("m", delta)
			continue
		}
		if len(model) <= -delta {
			model = "m"
			continue
		}
		model = model[:len(model)+delta]
	}
	return txs
}

func seedRefusalDiffs(t *testing.T, store *storage.SQLite, root []byte, n int) {
	t.Helper()
	txs := txsAbout500()
	stored := proto.Size(&types.DiffContent{Txs: txs})
	require.GreaterOrEqual(t, stored, 480)
	require.LessOrEqual(t, stored, 520)
	const batch = 1000
	recs := make([]types.DiffRecord, 0, batch)
	for nonce := 1; nonce <= n; nonce++ {
		recs = append(recs, types.DiffRecord{
			Diff: types.Diff{
				Nonce:         uint64(nonce),
				Txs:           txs,
				PostStateRoot: root,
			},
			StateHash: root,
		})
		if len(recs) == batch || nonce == n {
			require.NoError(t, store.AppendDiffBatch(context.Background(), refusalJournalEscrow, recs))
			recs = recs[:0]
		}
	}
}

func refusalPayloadRequest() transport.VerifyTimeoutRequest {
	return transport.VerifyTimeoutRequest{
		InferenceID: 1,
		Reason:      transport.TimeoutReasonToString(types.TimeoutReason_TIMEOUT_REASON_REFUSED),
		Payload: transport.PayloadToJSON(&host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   testutil.TestMaxTokens,
			StartedAt:   1000,
		}),
	}
}

// newRefusalJournalServer is a verifier whose store holds refusalJournalDiffs
// contiguous diffs and whose escrow state says the same tip. The live
// inference map stays one pending record; the journal is not replayed into it.
func newRefusalJournalServer(t *testing.T) (*transport.Server, []byte) {
	t.Helper()
	return newRefusalJournalServerN(t, refusalJournalDiffs)
}

// newRefusalJournalServerN is a verifier whose store holds n contiguous diffs
// and whose escrow state says the same tip. The live inference map stays one
// pending record; the journal is not replayed into it.
func newRefusalJournalServerN(t *testing.T, n int) (*transport.Server, []byte) {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(group))
	verifier := signing.NewSecp256k1Verifier()
	root := bytes.Repeat([]byte{0x11}, 32)

	store, err := storage.NewSQLite(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       refusalJournalEscrow,
		EpochID:        1,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))
	seedRefusalDiffs(t, store, root, n)

	sm, err := state.NewStateMachine(refusalJournalEscrow, config, group, 100000, user.Address(), verifier,
		testutil.MustMemoryStore(t, refusalJournalEscrow, user.Address(), config, group, 100000))
	require.NoError(t, err)
	st := sm.ExportState()
	st.LatestNonce = uint64(n)
	hash := testutil.TestPromptHash
	st.Inferences = map[uint64]*types.InferenceRecord{
		1: {
			Status:       types.StatusPending,
			ExecutorSlot: 1,
			ReservedCost: 150,
			PromptHash:   hash[:],
			Model:        "llama",
			InputLength:  100,
			MaxTokens:    testutil.TestMaxTokens,
		},
	}
	floor, err := heightsync.FloorIndexFromProto(heightsync.FloorConfig{}, sm.ExportHeightSyncFloor())
	require.NoError(t, err)
	require.NoError(t, sm.RestoreStateWithFloor(st, floor))
	statetest.ArmRefusalDue(t, sm, 1)

	h, err := host.NewHost(sm, hosts[0], stub.NewInferenceEngine(), refusalJournalEscrow, group, nil, host.WithStorage(store))
	require.NoError(t, err)
	t.Cleanup(h.Close)
	require.Equal(t, uint64(n), h.SnapshotState().LatestNonce)

	srv, err := transport.NewServer(h, store, verifier, user.Address())
	require.NoError(t, err)
	return srv, root
}

func assertBodiesUnderReadCap(t *testing.T, calls []recordedChallenge) {
	t.Helper()
	require.NotEmpty(t, calls)
	for _, c := range calls {
		require.LessOrEqualf(t, c.size, transport.DefaultRPCLargeReadMaxBytes, "ChallengeReceipt body is %d bytes", c.size)
	}
}

func assertColdJournalThenPayload(t *testing.T, calls []recordedChallenge) {
	t.Helper()
	assertNoncesThenPayload(t, calls, 1, refusalJournalDiffs)
}

// assertNoncesThenPayload requires one contiguous nonce run, each nonce once,
// and the payload on the last call only.
func assertNoncesThenPayload(t *testing.T, calls []recordedChallenge, from uint64, n int) {
	t.Helper()
	assertBodiesUnderReadCap(t, calls)
	var got []uint64
	for i, c := range calls {
		if i+1 < len(calls) {
			require.Falsef(t, c.payload, "call %d carries the payload before the journal ends", i)
			require.NotEmpty(t, c.nonces)
		}
		got = append(got, c.nonces...)
	}
	require.True(t, calls[len(calls)-1].payload)
	require.Len(t, got, n)
	for i, nonce := range got {
		require.Equalf(t, from+uint64(i), nonce, "nonce at position %d", i)
	}
}

func assertWalkHeap(t *testing.T, e *recordingExecutor) {
	t.Helper()
	require.NotZero(t, e.high)
	if e.high <= e.baseline {
		return
	}
	limit := uint64(largeJournalHeapPages) * host.ChallengePageMaxBytes
	over := e.high - e.baseline
	t.Logf("heap above seeded journal: %d bytes (limit %d)", over, limit)
	require.LessOrEqualf(t, over, limit, "retained heap during catch-up")
}

func runRefusalCheck(t *testing.T, srv *transport.Server, exec *recordingExecutor, req transport.VerifyTimeoutRequest) *transport.VerifyTimeoutResponse {
	t.Helper()
	srv.SetPeerClients(map[int]transport.HostPeerClient{1: exec})
	ctx, cancel := context.WithTimeout(context.Background(), host.RefusalVerifyBudget)
	defer cancel()
	start := time.Now()
	resp, err := srv.ServeVerifyTimeout(ctx, req)
	require.NoError(t, err)
	require.Less(t, time.Since(start), host.RefusalVerifyBudget)
	return resp
}

// TestRefusalChallengePagesStayUnderReadCap pins the refused-timeout
// ChallengeReceipt shape. The executor is at a matching tip, ahead of the
// verifier, or cold. No body may exceed the Connect read cap, and the cold
// executor is sent every nonce once, in order, then the payload.
// Today each case sends the journal in one request.
func TestRefusalChallengePagesStayUnderReadCap(t *testing.T) {
	srv, root := newRefusalJournalServer(t)
	req := refusalPayloadRequest()

	t.Run("matching root", func(t *testing.T) {
		exec := &recordingExecutor{t: t, nonce: 1, root: root}
		srv.SetPeerClients(map[int]transport.HostPeerClient{1: exec})
		resp, err := srv.ServeVerifyTimeout(context.Background(), req)
		require.NoError(t, err)
		require.True(t, resp.Accept)
		assertBodiesUnderReadCap(t, exec.calls)
	})
	t.Run("ahead of verifier", func(t *testing.T) {
		exec := &recordingExecutor{t: t, nonce: refusalJournalDiffs + 10, root: root}
		srv.SetPeerClients(map[int]transport.HostPeerClient{1: exec})
		resp, err := srv.ServeVerifyTimeout(context.Background(), req)
		require.NoError(t, err)
		require.True(t, resp.Accept)
		assertBodiesUnderReadCap(t, exec.calls)
	})
	t.Run("session not found", func(t *testing.T) {
		exec := &recordingExecutor{t: t, headErr: errors.New("session not found")}
		srv.SetPeerClients(map[int]transport.HostPeerClient{1: exec})
		resp, err := srv.ServeVerifyTimeout(context.Background(), req)
		require.NoError(t, err)
		require.True(t, resp.Accept)
		assertColdJournalThenPayload(t, exec.calls)
	})
}

// TestRefusalLargeJournal is the soak's journal, about 148,000 diffs of about
// 500 bytes, checked in process. HeapAlloc is sampled on every catch-up call
// after a GC, while that call's page is still reachable. The rise above the
// seeded journal stays within a few challenge pages. One refused-timeout
// check covers a down executor, one behind, and one caught up. A verify
// budget that expires returns the error and does not accept.
func TestRefusalLargeJournal(t *testing.T) {
	srv, root := newRefusalJournalServerN(t, refusalLargeJournalDiffs)
	runtime.GC()
	var seeded runtime.MemStats
	runtime.ReadMemStats(&seeded)
	baseline := seeded.HeapAlloc
	req := refusalPayloadRequest()
	n := refusalLargeJournalDiffs
	half := uint64(n / 2)

	// The deadline runs first. A finished cold walk remembers that it sent
	// the journal, and the next cold check would send the payload only.
	t.Run("deadline", func(t *testing.T) {
		exec := &recordingExecutor{
			t:            t,
			headErr:      errors.New("session not found"),
			block:        true,
			verifyBudget: time.Second,
		}
		srv.SetPeerClients(map[int]transport.HostPeerClient{1: exec})
		resp, err := srv.ServeVerifyTimeout(context.Background(), req)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Nil(t, resp)
	})
	t.Run("down", func(t *testing.T) {
		exec := &recordingExecutor{
			t:         t,
			headErr:   errors.New("session not found"),
			trackHeap: true,
			baseline:  baseline,
		}
		resp := runRefusalCheck(t, srv, exec, req)
		require.True(t, resp.Accept)
		assertNoncesThenPayload(t, exec.calls, 1, n)
		assertWalkHeap(t, exec)
	})
	t.Run("behind", func(t *testing.T) {
		exec := &recordingExecutor{
			t:         t,
			nonce:     half,
			root:      root,
			trackHeap: true,
			baseline:  baseline,
		}
		resp := runRefusalCheck(t, srv, exec, req)
		require.True(t, resp.Accept)
		assertNoncesThenPayload(t, exec.calls, half+1, n-int(half))
		assertWalkHeap(t, exec)
	})
	t.Run("caught up", func(t *testing.T) {
		exec := &recordingExecutor{
			t:         t,
			nonce:     uint64(n),
			root:      root,
			trackHeap: true,
			baseline:  baseline,
		}
		resp := runRefusalCheck(t, srv, exec, req)
		require.True(t, resp.Accept)
		require.Len(t, exec.calls, 1)
		require.Empty(t, exec.calls[0].nonces)
		require.True(t, exec.calls[0].payload)
		assertBodiesUnderReadCap(t, exec.calls)
		assertWalkHeap(t, exec)
	})
}
