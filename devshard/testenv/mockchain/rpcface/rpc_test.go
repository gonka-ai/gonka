package rpcface_test

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	cmttypes "github.com/cometbft/cometbft/types"
	gogoproto "github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"

	"devshard/cmd/devshardd/events"
	"devshard/testenv/mockchain/rpcface"
	"devshard/testenv/mockchain/seed"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

func startTestRPC(t *testing.T, interval time.Duration) (string, func()) {
	t.Helper()
	st := seed.Defaults()
	svc, url, cleanup, err := rpcface.NewInProcessServer(st, rpcface.Config{BlockInterval: interval})
	require.NoError(t, err)
	require.NotNil(t, svc)
	t.Cleanup(cleanup)
	return url, cleanup
}

func TestMockChainRPC_StatusAndBlock(t *testing.T) {
	url, _ := startTestRPC(t, time.Hour) // no block ticks during HTTP-only test

	client, err := rpchttp.New(url, "/websocket")
	require.NoError(t, err)

	status, err := client.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, "gonka-test", status.NodeInfo.Network)
	require.Equal(t, int64(150), status.SyncInfo.LatestBlockHeight)

	block, err := client.Block(context.Background(), &status.SyncInfo.LatestBlockHeight)
	require.NoError(t, err)
	require.Equal(t, int64(150), block.Block.Height)
	require.Equal(t, "gonka-test", block.Block.Header.ChainID)
}

func TestMockChainRPC_ListenerNewBlock(t *testing.T) {
	url, _ := startTestRPC(t, 50*time.Millisecond)

	l := events.NewListener(url)
	var mu sync.Mutex
	var blocks []events.NewBlockEvent
	l.OnNewBlock(func(_ context.Context, e events.NewBlockEvent) {
		mu.Lock()
		blocks = append(blocks, e)
		mu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- l.Start(ctx) }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(blocks) >= 1
	}, 2*time.Second, 20*time.Millisecond)

	mu.Lock()
	height := blocks[0].BlockHeight
	mu.Unlock()
	require.Greater(t, height, int64(150), "block ticker should advance past seed height")
}

func TestMockChainRPC_ListenerEscrowCreated(t *testing.T) {
	st := seed.Defaults()
	svc, url, cleanup, err := rpcface.NewInProcessServer(st, rpcface.Config{BlockInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(cleanup)

	l := events.NewListener(url)
	var mu sync.Mutex
	var created []events.DevshardEscrowCreatedEvent
	l.OnDevshardEscrowCreated(func(_ context.Context, e events.DevshardEscrowCreatedEvent) {
		mu.Lock()
		created = append(created, e)
		mu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go func() { _ = l.Start(ctx) }()

	// Allow websocket subscriptions to connect.
	time.Sleep(200 * time.Millisecond)

	err = svc.PutEscrowAndEmit(&inferencetypes.DevshardEscrow{
		Id:         99,
		Creator:    "gonka1creator",
		Amount:     500,
		EpochIndex: 1,
		Slots:      []string{"http://router:8080/devshard/v1"},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(created) == 1 && created[0].EscrowID == "99"
	}, 2*time.Second, 20*time.Millisecond)

	mu.Lock()
	e := created[0]
	mu.Unlock()
	require.Equal(t, "gonka1creator", e.Creator)
	require.Equal(t, uint64(500), e.Amount)
	require.Equal(t, uint64(1), e.EpochIndex)
}

func TestMockChainRPC_SignedCommitAndHeader(t *testing.T) {
	url, _ := startTestRPC(t, 50*time.Millisecond)

	client, err := rpchttp.New(url, "/websocket")
	require.NoError(t, err)

	status, err := client.Status(context.Background())
	require.NoError(t, err)
	h := status.SyncInfo.LatestBlockHeight
	require.Equal(t, int64(150), h)

	commit, err := client.Commit(context.Background(), &h)
	require.NoError(t, err)
	valsRes, err := client.Validators(context.Background(), &h, nil, nil)
	require.NoError(t, err)
	vals, err := cmttypes.ValidatorSetFromExistingValidators(valsRes.Validators)
	require.NoError(t, err)
	require.True(t, bytes.Equal(vals.Hash(), commit.Header.ValidatorsHash))
	require.NoError(t, vals.VerifyCommitLight(commit.Header.ChainID, commit.Commit.BlockID, commit.Header.Height, commit.Commit))

	hdr, err := client.Header(context.Background(), &h)
	require.NoError(t, err)
	require.Equal(t, commit.Header.Hash(), hdr.Header.Hash())

	info, err := client.BlockchainInfo(context.Background(), h, h)
	require.NoError(t, err)
	require.Len(t, info.BlockMetas, 1)
	require.Equal(t, commit.Header.Hash(), info.BlockMetas[0].Header.Hash())

	l := events.NewListener(url)
	var mu sync.Mutex
	var got events.NewBlockEvent
	l.OnNewBlock(func(_ context.Context, e events.NewBlockEvent) {
		mu.Lock()
		if got.BlockHeight == 0 {
			got = e
		}
		mu.Unlock()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = l.Start(ctx) }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return got.BlockHeight > 150 && len(got.Header) > 0
	}, 2*time.Second, 20*time.Millisecond)

	mu.Lock()
	live := got
	mu.Unlock()
	liveH := live.BlockHeight
	liveHdr, err := client.Header(context.Background(), &liveH)
	require.NoError(t, err)
	raw, err := gogoproto.Marshal(liveHdr.Header.ToProto())
	require.NoError(t, err)
	require.Equal(t, raw, live.Header)
	require.Equal(t, liveHdr.Header.Hash().Bytes(), live.BlockHash)
}

func TestMockChainRPC_ListenerEscrowSettled(t *testing.T) {
	st := seed.Defaults()
	svc, url, cleanup, err := rpcface.NewInProcessServer(st, rpcface.Config{BlockInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(cleanup)

	l := events.NewListener(url)
	var mu sync.Mutex
	var settled []events.DevshardEscrowSettledEvent
	l.OnDevshardEscrowSettled(func(_ context.Context, e events.DevshardEscrowSettledEvent) {
		mu.Lock()
		settled = append(settled, e)
		mu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go func() { _ = l.Start(ctx) }()
	time.Sleep(200 * time.Millisecond)

	err = svc.PublishEscrowSettled(1, "gonka1settler", 100, 5, 10)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(settled) == 1 && settled[0].EscrowID == "1"
	}, 2*time.Second, 20*time.Millisecond)
}
