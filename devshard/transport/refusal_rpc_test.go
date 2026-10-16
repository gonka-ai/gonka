package transport_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/heightsync"
	"devshard/host"
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
func (l retryRPCLookup) SessionForStartProof(id, addr string, _ []types.Diff, _ string) (rpcserver.SessionCore, error) {
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
	t       *testing.T
	nonce   uint64
	root    []byte
	headErr error
	calls   []recordedChallenge
}

func (e *recordingExecutor) SessionHead(context.Context) (uint64, []byte, error) {
	return e.nonce, e.root, e.headErr
}

func (e *recordingExecutor) GetMempool(context.Context) ([]*types.DevshardTx, error) {
	return nil, nil
}

func (e *recordingExecutor) ChallengeCatchUp(_ context.Context, inferenceID uint64, diffs []types.DiffWire, version string) error {
	raw, err := transport.ChallengeCatchUpBody(inferenceID, diffs, version)
	require.NoError(e.t, err)
	nonces := make([]uint64, len(diffs))
	for i, d := range diffs {
		nonces[i] = d.Nonce
	}
	e.calls = append(e.calls, recordedChallenge{size: len(raw), nonces: nonces})
	return nil
}

func (e *recordingExecutor) ChallengeReceipt(_ context.Context, inferenceID uint64, payload *host.InferencePayload, diffs []types.Diff) ([]byte, []*types.DevshardTx, error) {
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
	return nil, nil, nil
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
		InferenceID:     inferenceID,
		Payload:         transport.PayloadToJSON(payload),
		Diffs:           djList,
		ProtocolVersion: types.StartProtocolVersion(diffs),
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

func seedRefusalDiffs(t *testing.T, store *storage.SQLite, root []byte) {
	t.Helper()
	txs := txsAbout500()
	stored := proto.Size(&types.DiffContent{Txs: txs})
	require.GreaterOrEqual(t, stored, 480)
	require.LessOrEqual(t, stored, 520)
	const batch = 1000
	recs := make([]types.DiffRecord, 0, batch)
	for nonce := 1; nonce <= refusalJournalDiffs; nonce++ {
		recs = append(recs, types.DiffRecord{
			Diff: types.Diff{
				Nonce:         uint64(nonce),
				Txs:           txs,
				PostStateRoot: root,
			},
			StateHash: root,
		})
		if len(recs) == batch || nonce == refusalJournalDiffs {
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
	seedRefusalDiffs(t, store, root)

	sm, err := state.NewStateMachine(refusalJournalEscrow, config, group, 100000, user.Address(), verifier,
		testutil.MustMemoryStore(t, refusalJournalEscrow, user.Address(), config, group, 100000))
	require.NoError(t, err)
	st := sm.ExportState()
	st.LatestNonce = refusalJournalDiffs
	hash := testutil.TestPromptHash
	st.Inferences = map[uint64]*types.InferenceRecord{
		1: {
			Status:       types.StatusPending,
			ExecutorSlot: 1,
			ReservedCost: 150,
			StartedAt:    1000,
			PromptHash:   hash[:],
			Model:        "llama",
			InputLength:  100,
			MaxTokens:    testutil.TestMaxTokens,
		},
	}
	floor, err := heightsync.FloorIndexFromProto(heightsync.FloorConfig{}, sm.ExportHeightSyncFloor())
	require.NoError(t, err)
	require.NoError(t, sm.RestoreStateWithFloor(st, floor))

	h, err := host.NewHost(sm, hosts[0], stub.NewInferenceEngine(), refusalJournalEscrow, group, nil, host.WithStorage(store))
	require.NoError(t, err)
	t.Cleanup(h.Close)
	require.Equal(t, uint64(refusalJournalDiffs), h.SnapshotState().LatestNonce)

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
	require.Len(t, got, refusalJournalDiffs)
	for i, nonce := range got {
		require.Equalf(t, uint64(i+1), nonce, "nonce at position %d", i)
	}
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
