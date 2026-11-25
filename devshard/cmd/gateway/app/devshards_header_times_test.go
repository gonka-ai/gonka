package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"

	"devshard/bridge"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/heights"
	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
	"devshard/user"
)

const (
	headerTimeEscrowID     = "1"
	headerTimeKeyEnv       = "HEADER_TIME_ESCROW_KEY"
	headerTimeEscrowAmount = 5_000_000_000
	headerTimeHostCount    = 4
	headerTimeFloorHeight  = 700
	headerTimeBlockMs      = 6_000
	headerTimeMaxTokens    = 64
	headerTimePromptTokens = 1
)

type recordedBlockTimes struct {
	mu    sync.Mutex
	asked []uint64
}

func (times *recordedBlockTimes) HeaderTimeMs(height uint64, _ []byte) (int64, error) {
	times.mu.Lock()
	defer times.mu.Unlock()
	times.asked = append(times.asked, height)
	return int64(height) * headerTimeBlockMs, nil
}

func (times *recordedBlockTimes) askedFor(height uint64) bool {
	times.mu.Lock()
	defer times.mu.Unlock()
	return slices.Contains(times.asked, height)
}

type healthyRPCOracle struct {
	blocks.BlockOracle
	hash  []byte
	mu    sync.Mutex
	asked []uint64
}

func (rpc *healthyRPCOracle) AtWait(_ context.Context, height int64, _ time.Duration) (*blocks.Header, error) {
	rpc.mu.Lock()
	rpc.asked = append(rpc.asked, uint64(height))
	rpc.mu.Unlock()
	return &blocks.Header{Height: height, BlockHash: rpc.hash, Time: time.UnixMilli(height * headerTimeBlockMs)}, nil
}

func (rpc *healthyRPCOracle) askedFor(height uint64) bool {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	return slices.Contains(rpc.asked, height)
}

type downGRPCOracle struct {
	blocks.BlockOracle
	calls atomic.Int64
}

func (grpc *downGRPCOracle) HeaderAtWait(context.Context, int64, time.Duration) (*blocks.Header, error) {
	grpc.calls.Add(1)
	return nil, errors.New("chain grpc is unavailable")
}

type headerTimeFixture struct {
	hosts       []*signing.Secp256k1Signer
	user        signing.Signer
	chain       settledChain
	rows        rowLister
	storageRoot string
}

func newHeaderTimeFixture(t *testing.T) headerTimeFixture {
	t.Helper()
	keyHex := strings.Repeat("11", 32)
	t.Setenv(headerTimeKeyEnv, keyHex)
	creator, err := signing.SignerFromHex(keyHex)
	if err != nil {
		t.Fatalf("SignerFromHex() = %v, want nil", err)
	}
	hosts := make([]*signing.Secp256k1Signer, headerTimeHostCount)
	slots := make([]string, headerTimeHostCount)
	for index := range hosts {
		hosts[index] = testutil.MustGenerateKey(t)
		slots[index] = hosts[index].Address()
	}
	return headerTimeFixture{
		hosts:       hosts,
		user:        creator,
		chain:       settledChain{escrow: bridge.EscrowInfo{EscrowID: headerTimeEscrowID, CreatorAddress: creator.Address(), Slots: slots, Amount: headerTimeEscrowAmount, TokenPrice: 1}},
		rows:        rowLister{{EscrowID: headerTimeEscrowID, PrivateKeyEnv: headerTimeKeyEnv, Model: "model-a", Active: true}},
		storageRoot: t.TempDir(),
	}
}

func (fixture headerTimeFixture) openServing(t *testing.T, headerTimes state.HeaderTimeSource) *user.Session {
	t.Helper()
	handle, err := servingSessions(fixture.rows, fixture.storageRoot, fixture.chain, config.HeightSync{}, nil, headerTimes, nil, "/devshard/v2", nil, nil)(context.Background(), headerTimeEscrowID)
	if err != nil {
		t.Fatalf("serving session open = %v, want nil", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	return handle.UserSession()
}

func (fixture headerTimeFixture) raiseFloor(t *testing.T, machine *state.StateMachine, height uint64, hash []byte) {
	t.Helper()
	nonce := machine.LatestNonce() + 1
	heartbeat := &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		SlotsNum: headerTimeHostCount,
		Reason:   string(heightsync.ReasonQuietSession),
	}}}
	if _, err := machine.ApplyDiff(testutil.SignDiff(t, fixture.user, headerTimeEscrowID, nonce, []*types.DevshardTx{heartbeat})); err != nil {
		t.Fatalf("applying the heartbeat = %v, want nil", err)
	}
	const slot = 1
	ack := &types.MsgHeightAck{
		RefNonce:          nonce,
		SlotId:            slot,
		ObservedHeight:    height,
		ObservedBlockHash: hash,
		SyncState:         types.SyncState_SYNCED,
		PeerSeen:          []byte{0xff},
	}
	if err := heightsync.SignAck(fixture.hosts[slot], ack); err != nil {
		t.Fatalf("SignAck() = %v, want nil", err)
	}
	ackTx := &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}}
	if _, err := machine.ApplyDiff(testutil.SignDiff(t, fixture.user, headerTimeEscrowID, nonce+1, []*types.DevshardTx{ackTx})); err != nil {
		t.Fatalf("applying the host ack = %v, want nil", err)
	}
}

// Test flow:
//  1. Open a serving session over a block-time source that records the heights it is asked for.
//  2. Apply a heartbeat and a host ack that claims a height above the floor.
//  3. Assert the claim is applied and the floor is the claimed height.
//  4. Assert the source was asked for that height.
func TestAServingSessionRaisesTheFloorThroughItsHeaderTimeSource(t *testing.T) {
	fixture := newHeaderTimeFixture(t)
	times := &recordedBlockTimes{}
	machine := fixture.openServing(t, times).StateMachine()
	hash := []byte{0xaa}

	fixture.raiseFloor(t, machine, headerTimeFloorHeight, hash)

	floor, _, known := machine.HeightSyncFloorAsOf(machine.LatestNonce() + 1)
	if !known || floor != headerTimeFloorHeight {
		t.Fatalf("floor = %d, known = %v, want %d", floor, known, headerTimeFloorHeight)
	}
	if !times.askedFor(headerTimeFloorHeight) {
		t.Fatalf("the session never asked its source for height %d", headerTimeFloorHeight)
	}
}

// Test flow:
//  1. Open a serving session over a block-time source and raise the floor with a host ack.
//  2. Apply a start inference stamped with the floor.
//  3. Assert the record's start anchor is the block time the source gave for the floor.
//  4. Assert the refusal timeout is armed.
func TestAServingSessionArmsTheRefusalTimeoutAtTheFloorBlockTime(t *testing.T) {
	fixture := newHeaderTimeFixture(t)
	machine := fixture.openServing(t, &recordedBlockTimes{}).StateMachine()
	hash := []byte{0xaa}
	fixture.raiseFloor(t, machine, headerTimeFloorHeight, hash)
	nonce := machine.LatestNonce() + 1
	start := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
		InferenceId:       nonce,
		PromptHash:        []byte("prompt"),
		Model:             "model-a",
		InputLength:       headerTimePromptTokens,
		MaxTokens:         headerTimeMaxTokens,
		ObservedHeight:    headerTimeFloorHeight,
		ObservedBlockHash: hash,
	}}}

	if _, err := machine.ApplyDiff(testutil.SignDiff(t, fixture.user, headerTimeEscrowID, nonce, []*types.DevshardTx{start})); err != nil {
		t.Fatalf("applying the start = %v, want nil", err)
	}

	record, tracked := machine.GetInference(nonce)
	if !tracked || record.StartedAtTimeMs != headerTimeFloorHeight*headerTimeBlockMs {
		t.Fatalf("start anchor = %d, tracked = %v, want %d", record.StartedAtTimeMs, tracked, headerTimeFloorHeight*headerTimeBlockMs)
	}
	if _, err := machine.TimeoutRemaining(nonce, types.TimeoutReason_TIMEOUT_REASON_REFUSED); err != nil {
		t.Fatalf("TimeoutRemaining(refused) = %v, want an armed timeout", err)
	}
}

// Test flow:
//  1. Open and close a serving session, so the escrow has stored session metadata.
//  2. Open a read-only session over the same storage with a block-time source that records the heights it is asked for.
//  3. Apply a heartbeat and a host ack that claims a height above the floor.
//  4. Assert the floor is the claimed height and the source was asked for it.
func TestAReadOnlySessionRaisesTheFloorThroughItsHeaderTimeSource(t *testing.T) {
	fixture := newHeaderTimeFixture(t)
	if err := fixture.openServing(t, nil).Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	times := &recordedBlockTimes{}

	readOnly, err := readOnlySessions(fixture.rows, fixture.storageRoot, times)(context.Background(), headerTimeEscrowID)

	if err != nil {
		t.Fatalf("read-only session open = %v, want nil", err)
	}
	t.Cleanup(func() { _ = readOnly.Close() })
	machine := readOnly.UserSession().StateMachine()
	fixture.raiseFloor(t, machine, headerTimeFloorHeight, []byte{0xaa})
	floor, _, known := machine.HeightSyncFloorAsOf(machine.LatestNonce() + 1)
	if !known || floor != headerTimeFloorHeight {
		t.Fatalf("floor = %d, known = %v, want %d", floor, known, headerTimeFloorHeight)
	}
	if !times.askedFor(headerTimeFloorHeight) {
		t.Fatalf("the read-only session never asked its source for height %d", headerTimeFloorHeight)
	}
}

// Test flow:
//  1. Open a serving session over a header time source whose RPC oracle is healthy and whose gRPC oracle always fails, then raise the floor with a host ack.
//  2. Close it and reopen the serving session over a fresh source built the same way, as a restarted gateway would, and raise the floor to the next height.
//  3. Close that and open a read-only session over a third fresh source, and raise the floor once more.
//  4. Assert each session holds the height it raised, each source asked its own RPC oracle for that height, and the gRPC oracle was never called.
func TestSessionsOpenAndReopenWithChainGRPCDownAndRPCHealthy(t *testing.T) {
	fixture := newHeaderTimeFixture(t)
	hash := []byte{0xaa}
	grpc := &downGRPCOracle{}
	raiseFloorAndCheck := func(machine *state.StateMachine, rpc *healthyRPCOracle, height uint64) {
		t.Helper()
		fixture.raiseFloor(t, machine, height, hash)
		floor, _, known := machine.HeightSyncFloorAsOf(machine.LatestNonce() + 1)
		if !known || floor != height {
			t.Fatalf("floor = %d, known = %v, want %d", floor, known, height)
		}
		if !rpc.askedFor(height) {
			t.Fatalf("the source never asked the RPC oracle for height %d", height)
		}
	}
	firstRPC := &healthyRPCOracle{hash: hash}
	first := fixture.openServing(t, heights.NewHeaderTimes(&heights.Oracle{BlockOracle: grpc}, nil, firstRPC))
	raiseFloorAndCheck(first.StateMachine(), firstRPC, headerTimeFloorHeight)
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}

	secondRPC := &healthyRPCOracle{hash: hash}
	second := fixture.openServing(t, heights.NewHeaderTimes(&heights.Oracle{BlockOracle: grpc}, nil, secondRPC))
	raiseFloorAndCheck(second.StateMachine(), secondRPC, headerTimeFloorHeight+1)
	if err := second.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}

	thirdRPC := &healthyRPCOracle{hash: hash}
	readOnly, err := readOnlySessions(fixture.rows, fixture.storageRoot, heights.NewHeaderTimes(&heights.Oracle{BlockOracle: grpc}, nil, thirdRPC))(context.Background(), headerTimeEscrowID)
	if err != nil {
		t.Fatalf("read-only session open = %v, want nil", err)
	}
	t.Cleanup(func() { _ = readOnly.Close() })
	raiseFloorAndCheck(readOnly.UserSession().StateMachine(), thirdRPC, headerTimeFloorHeight+2)

	if calls := grpc.calls.Load(); calls != 0 {
		t.Fatalf("the gRPC oracle was called %d times, want 0", calls)
	}
}
