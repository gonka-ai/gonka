package host

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
	"devshard/chainoracle/blocks/headertime"
	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

type headerChain struct {
	mu      sync.Mutex
	headers map[int64]*blocks.Header
}

func newHeaderChain() *headerChain {
	return &headerChain{headers: make(map[int64]*blocks.Header)}
}

func (c *headerChain) add(height int64, hash []byte, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.headers[height] = blocks.HashOnlyHeader(height, at, "fake-chain", hash)
}

func (c *headerChain) Latest(context.Context) (*blocks.Header, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var top *blocks.Header
	for _, h := range c.headers {
		if top == nil || h.Height > top.Height {
			top = h
		}
	}
	if top == nil {
		return nil, blocks.ErrHeaderNotFound
	}
	return top, nil
}

func (c *headerChain) At(_ context.Context, height int64) (*blocks.Header, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h, ok := c.headers[height]; ok {
		return h, nil
	}
	return blocks.DummyHeader(height), nil
}

func (c *headerChain) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (c *headerChain) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	return nil, errFakeOracleNotImpl
}

func TestTimeoutVoteNeedsTheFloorBlockOnTheLocalChain(t *testing.T) {
	for _, tc := range []struct {
		name      string
		chainHash []byte
		wantErr   error
	}{
		{name: "fabricated height never lands", wantErr: types.ErrHeaderPending},
		{name: "height lands on another block", chainHash: []byte{0xbb}, wantErr: types.ErrHeaderMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
			user := testutil.MustGenerateKey(t)
			group := testutil.MakeGroup(hosts)
			config := testutil.DefaultConfig(len(hosts))
			config.ExecutionTimeout = 1
			anchorAt := time.Unix(1_800_000_000, 0)
			hash, farHash := []byte{0xaa}, []byte{0xcc}
			chain := newHeaderChain()
			chain.add(100, hash, anchorAt)
			sm, err := state.NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(),
				testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1_000_000),
				state.WithHeaderTimes(headertime.New(chain)))
			require.NoError(t, err)
			h, err := NewHost(sm, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil, WithChainOracle(chain))
			require.NoError(t, err)

			heartbeat := &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
				SlotsNum: uint64(len(hosts)), Reason: string(heightsync.ReasonQuietSession),
			}}}
			ack := func(ref, height uint64, blockHash []byte) *types.DevshardTx {
				msg := &types.MsgHeightAck{RefNonce: ref, SlotId: 2, ObservedHeight: height, ObservedBlockHash: blockHash,
					SyncState: types.SyncState_SYNCED, PeerSeen: []byte{0xff}}
				require.NoError(t, heightsync.SignAck(hosts[2], msg))
				return &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: msg}}
			}
			const id uint64 = 4
			start := testutil.StartTx(id)
			start.GetStartInference().ObservedHeight = 100
			start.GetStartInference().ObservedBlockHash = hash
			receipt := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", id, testutil.TestPromptHash[:], "llama", 100, testutil.TestMaxTokens,
				testutil.ReceiptStamp{Height: 100, Hash: hash})
			confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
				InferenceId: id, ExecutorSig: receipt, ObservedHeight: 100, ObservedBlockHash: hash,
			}}}
			for n, txs := range [][]*types.DevshardTx{{heartbeat}, {ack(1, 100, hash)}, {heartbeat}, {start}, {confirm}, {heartbeat}} {
				_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", uint64(n+1), txs))
				require.NoError(t, err)
			}
			rec, ok := sm.GetInference(id)
			require.True(t, ok)
			require.Equal(t, types.StatusStarted, rec.Status)
			require.Equal(t, anchorAt.UnixMilli(), rec.ConfirmedAtTimeMs, "the confirm anchor carries the block time of its floor")

			if tc.chainHash != nil {
				chain.add(1_000_000, tc.chainHash, anchorAt.Add(time.Hour))
			}
			_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 7, []*types.DevshardTx{ack(6, 1_000_000, farHash)}))
			require.ErrorIs(t, err, tc.wantErr)
			floor, _, _ := sm.HeightSyncFloorAsOf(7)
			require.EqualValues(t, 100, floor)
			require.ErrorIs(t, h.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_EXECUTION), types.ErrTimeoutNotDue)

			if tc.chainHash != nil {
				return
			}
			chain.add(1_000_000, farHash, anchorAt.Add(500*time.Millisecond))
			_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 7, []*types.DevshardTx{ack(6, 1_000_000, farHash)}))
			require.NoError(t, err)
			require.ErrorIs(t, h.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_EXECUTION), types.ErrTimeoutNotDue,
				"the window is block time, not the distance in heights")
		})
	}
}

func TestApplyWaitsForAFloorBlockTheLocalChainIsAboutToSee(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	hash := []byte{0xaa}
	chain := newHeaderChain()
	sm, err := state.NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1_000_000),
		state.WithHeaderTimes(headertime.New(chain)))
	require.NoError(t, err)
	h, err := NewHost(sm, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil, WithChainOracle(chain))
	require.NoError(t, err)

	heartbeat := &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		SlotsNum: uint64(len(hosts)), Reason: string(heightsync.ReasonQuietSession),
	}}}
	ackMsg := &types.MsgHeightAck{RefNonce: 1, SlotId: 2, ObservedHeight: 100, ObservedBlockHash: hash,
		SyncState: types.SyncState_SYNCED, PeerSeen: []byte{0xff}}
	require.NoError(t, heightsync.SignAck(hosts[2], ackMsg))
	ack := &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: ackMsg}}
	start := testutil.StartTx(3)
	start.GetStartInference().ObservedHeight = 100
	start.GetStartInference().ObservedBlockHash = hash
	diffs := []types.Diff{
		testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{heartbeat}),
		testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{ack}),
		testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{start}),
	}

	go func() {
		time.Sleep(300 * time.Millisecond)
		chain.add(100, hash, time.Unix(1_800_000_000, 0))
	}()
	h.mu.Lock()
	applied, err := h.applyDiffsAwaitingHeadersLocked(context.Background(), diffs)
	h.mu.Unlock()
	require.NoError(t, err, "a host one block behind waits for its node instead of rejecting the diff")
	require.NotEmpty(t, applied)
	require.Equal(t, uint64(3), sm.LatestNonce())
	rec, ok := sm.GetInference(3)
	require.True(t, ok)
	require.Equal(t, time.Unix(1_800_000_000, 0).UnixMilli(), rec.StartedAtTimeMs)
}

func TestExecutionVoteDefersOnAFinishWhoseBlockIsAboutToLand(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	config.ExecutionTimeout = 1
	anchorAt := time.Unix(1_800_000_000, 0)
	hash, floorHash := []byte{0xaa}, []byte{0xbb}
	chain := newHeaderChain()
	chain.add(100, hash, anchorAt)
	chain.add(200, floorHash, anchorAt.Add(time.Hour))
	sm, err := state.NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1_000_000),
		state.WithHeaderTimes(headertime.New(chain)))
	require.NoError(t, err)
	h, err := NewHost(sm, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil, WithChainOracle(chain))
	require.NoError(t, err)

	heartbeat := &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		SlotsNum: uint64(len(hosts)), Reason: string(heightsync.ReasonQuietSession),
	}}}
	ack := func(ref, height uint64, blockHash []byte) *types.DevshardTx {
		msg := &types.MsgHeightAck{RefNonce: ref, SlotId: 2, ObservedHeight: height, ObservedBlockHash: blockHash,
			SyncState: types.SyncState_SYNCED, PeerSeen: []byte{0xff}}
		require.NoError(t, heightsync.SignAck(hosts[2], msg))
		return &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: msg}}
	}
	const id uint64 = 4
	start := testutil.StartTx(id)
	start.GetStartInference().ObservedHeight = 100
	start.GetStartInference().ObservedBlockHash = hash
	receipt := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", id, testutil.TestPromptHash[:], "llama", 100, testutil.TestMaxTokens,
		testutil.ReceiptStamp{Height: 100, Hash: hash})
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: id, ExecutorSig: receipt, ObservedHeight: 100, ObservedBlockHash: hash,
	}}}
	for n, txs := range [][]*types.DevshardTx{{heartbeat}, {ack(1, 100, hash)}, {heartbeat}, {start}, {confirm}, {heartbeat}, {ack(6, 200, floorHash)}} {
		_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", uint64(n+1), txs))
		require.NoError(t, err)
	}
	require.NoError(t, h.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_EXECUTION))
	st, rec, ok := sm.SnapshotWithInference(id)
	require.True(t, ok)

	finishAt := func(height uint64, blockHash []byte) []*types.DevshardTx {
		msg := &types.MsgFinishInference{
			InferenceId: id, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash,
			InputTokens: 10, OutputTokens: 20, ExecutorSlot: rec.ExecutorSlot, EscrowId: "escrow-1",
			ObservedHeight: height, ObservedBlockHash: blockHash,
		}
		msg.ProposerSig = testutil.SignProposerTx(t, hosts[rec.ExecutorSlot], msg)
		return []*types.DevshardTx{{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}}
	}

	nearHash := []byte{0xcc}
	accept, err := VerifyExecutionTimeout(context.Background(), st, id, rec, finishAt(201, nearHash), nil, nil, h)
	require.ErrorIs(t, err, types.ErrHeaderPending, "a Finish one block past our tip defers the vote")
	require.False(t, accept)

	accept, err = VerifyExecutionTimeout(context.Background(), st, id, rec, finishAt(1_000_000, []byte{0xdd}), nil, nil, h)
	require.NoError(t, err)
	require.True(t, accept, "a height far past our tip is not evidence and cannot stall the timeout")

	later := time.Now().Add(PendingEvidenceDeferWindow + time.Second)
	h.evidenceDeferrals.clock = func() time.Time { return later }
	accept, err = VerifyExecutionTimeout(context.Background(), st, id, rec, finishAt(202, []byte{0xce}), nil, nil, h)
	require.NoError(t, err)
	require.True(t, accept, "after the window a new near-tip stamp no longer defers the vote")
	h.evidenceDeferrals.clock = nil

	chain.add(201, nearHash, anchorAt.Add(time.Hour+5*time.Second))
	accept, err = VerifyExecutionTimeout(context.Background(), st, id, rec, finishAt(201, nearHash), nil, nil, h)
	require.NoError(t, err)
	require.False(t, accept, "once the block lands the Finish rejects the timeout")
}

func TestMovingFutureStampsCannotDeferAVoteForever(t *testing.T) {
	var d evidenceDeferrals
	now := time.Unix(1_800_000_000, 0)
	d.clock = func() time.Time { return now }
	require.True(t, d.allow(4, types.TimeoutReason_TIMEOUT_REASON_EXECUTION))
	for i := 0; i < 20; i++ {
		now = now.Add(5 * time.Second)
		if now.Sub(time.Unix(1_800_000_000, 0)) <= PendingEvidenceDeferWindow {
			require.True(t, d.allow(4, types.TimeoutReason_TIMEOUT_REASON_EXECUTION))
		} else {
			require.False(t, d.allow(4, types.TimeoutReason_TIMEOUT_REASON_EXECUTION), "a re-signed stamp does not renew the window")
		}
	}
	require.True(t, d.allow(5, types.TimeoutReason_TIMEOUT_REASON_EXECUTION), "another inference has its own window")
}

type pendingExecutor struct{ calls int }

func (p *pendingExecutor) GetMempool(context.Context) ([]*types.DevshardTx, error) { return nil, nil }

func (p *pendingExecutor) ChallengeCatchUp(context.Context, uint64, []types.DiffWire, string) error {
	return fmt.Errorf("challenge-receipt: %w", types.ErrHeaderPending)
}

func (p *pendingExecutor) ChallengeReceipt(context.Context, uint64, *InferencePayload, []types.Diff) ([]byte, []*types.DevshardTx, error) {
	p.calls++
	return nil, nil, fmt.Errorf("challenge-receipt: %w", types.ErrHeaderPending)
}

func TestExecutorHeaderPendingCannotHoldAnExecutionTimeoutForever(t *testing.T) {
	st := stateWithStarted(1, 1)
	now := time.Unix(1_800_000_000, 0)
	judge := windowedJudge{EvidenceVerifier: due(newEvidenceGroup(t).sm), deferrals: &evidenceDeferrals{clock: func() time.Time { return now }}}
	executor := &pendingExecutor{}

	for round := 0; round < 5; round++ {
		accept, err := VerifyExecutionTimeout(context.Background(), st, 1, st.Inferences[1], nil, executor, nil, judge)
		require.ErrorIs(t, err, types.ErrHeaderPending, "inside the window the executor's wait defers the vote")
		require.False(t, accept)
		now = now.Add(10 * time.Second)
	}
	now = now.Add(PendingEvidenceDeferWindow)
	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, st.Inferences[1], nil, executor, nil, judge)
	require.NoError(t, err)
	require.True(t, accept, "after the window the unverifiable answer counts as unreachable, as before")
}

func TestRefusalWaitDoesNotSpendTheExecutionWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	judge := windowedJudge{EvidenceVerifier: due(newEvidenceGroup(t).sm), deferrals: &evidenceDeferrals{clock: func() time.Time { return now }}}
	pst := stateWithPendingFull(1, 1)
	refuser := &progressPeer{steps: []struct {
		receipt []byte
		mempool []*types.DevshardTx
		err     error
	}{{err: fmt.Errorf("challenge-receipt: %w", types.ErrHeaderPending)}}}
	accept, err := VerifyRefusedTimeout(context.Background(), pst, 1, pst.Inferences[1], testPayload(), nil, refuser, nil, judge)
	require.ErrorIs(t, err, types.ErrHeaderPending)
	require.False(t, accept)

	now = now.Add(5 * time.Minute)
	sst := stateWithStarted(1, 1)
	accept, err = VerifyExecutionTimeout(context.Background(), sst, 1, sst.Inferences[1], nil, &pendingExecutor{}, nil, judge)
	require.ErrorIs(t, err, types.ErrHeaderPending, "the execution stage opens its own window")
	require.False(t, accept)
}

type headerPendingEvidence struct {
	EvidenceVerifier
	h *Host
}

func (p headerPendingEvidence) CheckEvidence(*types.InferenceRecord, ...*types.DevshardTx) error {
	return fmt.Errorf("evidence: %w", types.ErrHeaderPending)
}

func (p headerPendingEvidence) PendingEvidenceDeferrable(reason types.TimeoutReason, txs ...*types.DevshardTx) bool {
	return p.h.PendingEvidenceDeferrable(reason, txs...)
}

func (p headerPendingEvidence) HeaderPendingDeferrable(id uint64, reason types.TimeoutReason) bool {
	return p.h.HeaderPendingDeferrable(id, reason)
}

func TestPendingConfirmDoesNotSpendThePendingFinishWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h := &Host{}
	h.evidenceDeferrals.clock = func() time.Time { return now }
	judge := headerPendingEvidence{EvidenceVerifier: due(nil), h: h}

	pst := stateWithPendingFull(1, 1)
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ExecutorSig: []byte{1}}}}
	accept, err := VerifyRefusedTimeout(context.Background(), pst, 1, pst.Inferences[1], testPayload(), []*types.DevshardTx{confirm}, nil, nil, judge)
	require.ErrorIs(t, err, types.ErrHeaderPending)
	require.False(t, accept)

	now = now.Add(32 * time.Minute)
	sst := stateWithStarted(1, 1)
	finish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 1, EscrowId: "escrow-1", ExecutorSlot: 1}}}
	accept, err = VerifyExecutionTimeout(context.Background(), sst, 1, sst.Inferences[1], []*types.DevshardTx{finish}, nil, nil, judge)
	require.ErrorIs(t, err, types.ErrHeaderPending, "a Finish waiting for its block opens the execution window")
	require.False(t, accept)
}
