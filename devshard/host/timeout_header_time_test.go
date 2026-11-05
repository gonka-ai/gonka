package host

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
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
			src := headertime.New(headertime.WithFiller(headertime.OracleFiller(chain, headertime.OriginChain, false)))
			stored, err := chain.At(context.Background(), 100)
			require.NoError(t, err)
			src.Observe(stored, headertime.OriginChain, false)
			sm, err := state.NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(),
				testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1_000_000),
				state.WithHeaderTimes(src))
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
			require.Zero(t, rec.ConfirmedAtTimeMs)

			if tc.chainHash != nil {
				chain.add(1_000_000, tc.chainHash, anchorAt.Add(time.Hour))
			}
			// A committed ack raises F even when the local chain has not
			// caught that height, or has a different hash there. The timeout
			// vote still needs that block.
			_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 7, []*types.DevshardTx{ack(6, 1_000_000, farHash)}))
			require.NoError(t, err)
			floor, _, _ := sm.HeightSyncFloorAsOf(8)
			require.EqualValues(t, 1_000_000, floor)
			require.ErrorIs(t, h.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_EXECUTION), tc.wantErr)

			if tc.chainHash != nil {
				return
			}
			chain.add(1_000_000, farHash, anchorAt.Add(500*time.Millisecond))
			require.ErrorIs(t, h.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_EXECUTION), types.ErrTimeoutNotDue,
				"the window is the block time, not the distance in heights")
		})
	}
}

type countingHeaderSource struct {
	*headertime.Source
	calls atomic.Int32
}

func (c *countingHeaderSource) HeaderTimeMs(height uint64, hash []byte) (int64, error) {
	c.calls.Add(1)
	return c.Source.HeaderTimeMs(height, hash)
}

func (c *countingHeaderSource) AwaitHeaderTimeMs(ctx context.Context, height uint64, hash []byte) (int64, error) {
	c.calls.Add(1)
	return c.Source.AwaitHeaderTimeMs(ctx, height, hash)
}

func TestSignPathDoesNotWaitForTheHeaderMap(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	hash := []byte{0xaa}
	chain := newHeaderChain()
	src := &countingHeaderSource{Source: headertime.New(headertime.WithFiller(headertime.OracleFiller(chain, headertime.OriginChain, false)))}
	sm, err := state.NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1_000_000),
		state.WithHeaderTimes(src))
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
	const id uint64 = 3
	start := testutil.StartTx(id)
	start.GetStartInference().ObservedHeight = 100
	start.GetStartInference().ObservedBlockHash = hash

	started := time.Now()
	for n, txs := range [][]*types.DevshardTx{{heartbeat}, {ack}, {start}} {
		_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", uint64(n+1), txs))
		require.NoError(t, err)
	}
	require.Less(t, time.Since(started), 200*time.Millisecond, "apply does not sleep on a missing header time")
	require.Zero(t, src.calls.Load())
	rec, ok := sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, types.StatusPending, rec.Status)
	require.Zero(t, rec.StartedAtTimeMs)

	h.mu.Lock()
	confirm, err := h.confirmStartLocked(id, rec, nil, nil)
	h.mu.Unlock()
	require.NoError(t, err)
	require.NotNil(t, confirm)
	require.Equal(t, uint64(100), confirm.ObservedHeight)
	require.Zero(t, src.calls.Load(), "signing the receipt does not read HeaderTimeMs")

	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 4, []*types.DevshardTx{{
		Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: confirm},
	}}))
	require.NoError(t, err)
	require.Zero(t, src.calls.Load())
	rec, ok = sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status)
	require.Zero(t, rec.ConfirmedAtTimeMs)

	finish := &types.MsgFinishInference{
		InferenceId: id, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash,
		InputTokens: 10, OutputTokens: 20, ExecutorSlot: rec.ExecutorSlot, EscrowId: "escrow-1",
		ObservedHeight: 100, ObservedBlockHash: hash,
	}
	finish.ProposerSig = testutil.SignProposerTx(t, hosts[rec.ExecutorSlot], finish)
	started = time.Now()
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 5, []*types.DevshardTx{{
		Tx: &types.DevshardTx_FinishInference{FinishInference: finish},
	}}))
	require.NoError(t, err)
	require.Less(t, time.Since(started), 200*time.Millisecond, "Finish applies once, with the map still empty")
	require.Zero(t, src.calls.Load())
	rec, _ = sm.GetInference(id)
	require.Equal(t, types.StatusFinished, rec.Status)

	h.mu.Lock()
	_, _, _, err = h.signIfAccepted(nil)
	h.mu.Unlock()
	require.NoError(t, err)
	require.Zero(t, src.calls.Load(), "signing the diff does not read HeaderTimeMs")
}

func TestExecutionVoteRejectsOnAFinishWhileThatHeightIsMissing(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	config.ExecutionTimeout = 1
	anchorAt := time.Now().Add(-2 * time.Second)
	hash, floorHash := []byte{0xaa}, []byte{0xbb}
	chain := newHeaderChain()
	chain.add(100, hash, anchorAt)
	chain.add(200, floorHash, anchorAt.Add(time.Hour))
	src := headertime.New(headertime.WithFiller(headertime.OracleFiller(chain, headertime.OriginChain, false)))
	for _, height := range []int64{100, 200} {
		stored, err := chain.At(context.Background(), height)
		require.NoError(t, err)
		src.Observe(stored, headertime.OriginChain, false)
	}
	sm, err := state.NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1_000_000),
		state.WithHeaderTimes(src))
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

	started := time.Now()
	accept, err := VerifyExecutionTimeout(context.Background(), st, id, rec, finishAt(201, []byte{0xcc}), nil, nil, h)
	require.NoError(t, err)
	require.False(t, accept, "a Finish the log accepts rejects the timeout while that height is missing from the map")
	require.Less(t, time.Since(started), 200*time.Millisecond)

	accept, err = VerifyExecutionTimeout(context.Background(), st, id, rec, finishAt(1_000_000, []byte{0xdd}), nil, nil, h)
	require.NoError(t, err)
	require.False(t, accept, "a signed Finish is evidence from the stamp and the log")
}

type pendingExecutor struct{ calls int }

func (p *pendingExecutor) GetMempool(context.Context) ([]*types.DevshardTx, error) { return nil, nil }

func (p *pendingExecutor) ChallengeCatchUp(context.Context, uint64, []types.DiffWire, string) error {
	return fmt.Errorf("challenge-receipt: %w", net.ErrClosed)
}

func (p *pendingExecutor) ChallengeReceipt(context.Context, uint64, *InferencePayload, []types.Diff) ([]byte, []*types.DevshardTx, error) {
	p.calls++
	return nil, nil, fmt.Errorf("challenge-receipt: %w", net.ErrClosed)
}

func TestExecutorUnreachableAcceptsTheTimeout(t *testing.T) {
	st := stateWithStarted(1, 1)
	executor := &pendingExecutor{}
	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, st.Inferences[1], nil, executor, nil, due(newEvidenceGroup(t).sm))
	require.NoError(t, err)
	require.True(t, accept, "an executor that cannot serve has no finish, so the timeout stands")
	require.Equal(t, 1, executor.calls)
}

func TestUnreachableAcceptsRefusalAndExecution(t *testing.T) {
	ev := due(newEvidenceGroup(t).sm)
	pst := stateWithPendingFull(1, 1)
	refuser := &progressPeer{steps: []struct {
		receipt []byte
		mempool []*types.DevshardTx
		err     error
	}{{err: fmt.Errorf("challenge-receipt: %w", net.ErrClosed)}}}
	accept, err := VerifyRefusedTimeout(context.Background(), pst, 1, pst.Inferences[1], testPayload(), nil, refuser, nil, ev)
	require.NoError(t, err)
	require.True(t, accept)

	sst := stateWithStarted(1, 1)
	executor := &pendingExecutor{}
	accept, err = VerifyExecutionTimeout(context.Background(), sst, 1, sst.Inferences[1], nil, executor, nil, ev)
	require.NoError(t, err)
	require.True(t, accept)
	require.Equal(t, 1, executor.calls)
}

type failingEvidence struct{ EvidenceVerifier }

func (failingEvidence) CheckEvidence(*types.InferenceRecord, ...*types.DevshardTx) error {
	return fmt.Errorf("evidence: %w", types.ErrHeaderPending)
}

func TestFailedEvidenceDoesNotWithholdTheVote(t *testing.T) {
	ev := failingEvidence{EvidenceVerifier: due(nil)}
	pst := stateWithPendingFull(1, 1)
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ExecutorSig: []byte{1}}}}
	accept, err := VerifyRefusedTimeout(context.Background(), pst, 1, pst.Inferences[1], testPayload(), []*types.DevshardTx{confirm}, nil, nil, ev)
	require.NoError(t, err)
	require.True(t, accept, "a confirm the host cannot check is not evidence")

	sst := stateWithStarted(1, 1)
	finish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 1, EscrowId: "escrow-1", ExecutorSlot: 1}}}
	accept, err = VerifyExecutionTimeout(context.Background(), sst, 1, sst.Inferences[1], []*types.DevshardTx{finish}, nil, nil, ev)
	require.NoError(t, err)
	require.True(t, accept, "a finish the host cannot check is not evidence")
}
