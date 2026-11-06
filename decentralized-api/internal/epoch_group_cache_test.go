package internal

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"decentralized-api/cosmosclient"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

type mutableEpochGroupQueryServer struct {
	types.UnimplementedQueryServer
	mu   sync.Mutex
	data types.EpochGroupData
}

func (s *mutableEpochGroupQueryServer) EpochGroupData(
	_ context.Context,
	_ *types.QueryGetEpochGroupDataRequest,
) (*types.QueryGetEpochGroupDataResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &types.QueryGetEpochGroupDataResponse{EpochGroupData: s.data}, nil
}

func (s *mutableEpochGroupQueryServer) set(data types.EpochGroupData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = data
}

func startEpochGroupCacheTestServer(t *testing.T, srv types.QueryServer) (*grpc.ClientConn, func()) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	types.RegisterQueryServer(server, srv)
	go func() { _ = server.Serve(listener) }()
	dialer := func(context.Context, string) (net.Conn, error) { return listener.Dial() }
	conn, err := grpc.DialContext(context.Background(), "bufnet", grpc.WithContextDialer(dialer), grpc.WithInsecure())
	require.NoError(t, err)
	cleanup := func() { server.Stop(); _ = listener.Close(); _ = conn.Close() }
	return conn, cleanup
}

func TestIsActiveParticipant_RefetchesEmptyCachedEpoch(t *testing.T) {
	queryServer := &mutableEpochGroupQueryServer{
		data: types.EpochGroupData{
			// Epoch start: models may exist before ValidationWeights are assigned.
			SubGroupModels: []string{"Qwen/Qwen2.5-7B-Instruct"},
		},
	}
	conn, cleanup := startEpochGroupCacheTestServer(t, queryServer)
	t.Cleanup(cleanup)

	mockCosmos := &cosmosclient.MockCosmosMessageClient{}
	mockCosmos.On("NewInferenceQueryClient").Return(types.NewQueryClient(conn))

	cache := NewEpochGroupDataCache(mockCosmos)

	// First fetch (as refreshModelValidationThresholds does at epoch start) caches 0 participants.
	_, err := cache.GetEpochGroupData(context.Background(), 2)
	require.NoError(t, err)

	active, err := cache.IsActiveParticipant(context.Background(), 2, "gonka1participant")
	require.NoError(t, err)
	require.False(t, active)

	// Later, members are assigned on-chain.
	queryServer.set(types.EpochGroupData{
		SubGroupModels: []string{"Qwen/Qwen2.5-7B-Instruct"},
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: "gonka1participant"},
		},
	})

	active, err = cache.IsActiveParticipant(context.Background(), 2, "gonka1participant")
	require.NoError(t, err)
	require.True(t, active, "empty pre-member cache must be refreshed once ValidationWeights exist")
}

type blockingEpochQueryClient struct {
	types.QueryClient
	calls        atomic.Int32
	entered      chan struct{}
	release      chan struct{}
	blockEpoch   uint64
	blockAll     bool
	ignoreCtx    bool
	sawDeadline  atomic.Bool
	sawCancelled atomic.Bool
	result       types.EpochGroupData
	err          error
}

func newBlockingEpochQueryClient(blockEpoch uint64) *blockingEpochQueryClient {
	return &blockingEpochQueryClient{
		entered:    make(chan struct{}, 64),
		release:    make(chan struct{}),
		blockEpoch: blockEpoch,
		result: types.EpochGroupData{
			ValidationWeights: []*types.ValidationWeight{{MemberAddress: "gonka1participant"}},
		},
	}
}

func (q *blockingEpochQueryClient) block(ctx context.Context) {
	if _, ok := ctx.Deadline(); ok {
		q.sawDeadline.Store(true)
	}
	q.entered <- struct{}{}
	if q.ignoreCtx {
		<-q.release
		return
	}
	select {
	case <-q.release:
	case <-ctx.Done():
		q.sawCancelled.Store(true)
	}
}

func (q *blockingEpochQueryClient) EpochGroupData(ctx context.Context, in *types.QueryGetEpochGroupDataRequest, _ ...grpc.CallOption) (*types.QueryGetEpochGroupDataResponse, error) {
	q.calls.Add(1)
	if q.blockAll || in.EpochIndex == q.blockEpoch {
		q.block(ctx)
	}
	if q.err != nil {
		return nil, q.err
	}
	return &types.QueryGetEpochGroupDataResponse{EpochGroupData: q.result}, nil
}

func (q *blockingEpochQueryClient) CurrentEpochGroupData(ctx context.Context, _ *types.QueryCurrentEpochGroupDataRequest, _ ...grpc.CallOption) (*types.QueryCurrentEpochGroupDataResponse, error) {
	q.calls.Add(1)
	q.block(ctx)
	if q.err != nil {
		return nil, q.err
	}
	return &types.QueryCurrentEpochGroupDataResponse{EpochGroupData: q.result}, nil
}

func newBlockingEpochCache(q *blockingEpochQueryClient) *EpochGroupDataCache {
	mockCosmos := &cosmosclient.MockCosmosMessageClient{}
	mockCosmos.On("NewInferenceQueryClient").Return(q)
	return NewEpochGroupDataCache(mockCosmos)
}

func waitEntered(t *testing.T, q *blockingEpochQueryClient) {
	t.Helper()
	select {
	case <-q.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("query did not start")
	}
}

func TestEpochCache_HungQueryDoesNotBlockOtherEpochs(t *testing.T) {
	q := newBlockingEpochQueryClient(7)
	cache := newBlockingEpochCache(q)

	_, err := cache.GetEpochGroupData(context.Background(), 6)
	require.NoError(t, err)

	hung := make(chan struct{})
	go func() {
		defer close(hung)
		_, _ = cache.GetEpochGroupData(context.Background(), 7)
	}()
	waitEntered(t, q)

	type otherResult struct {
		active bool
		err    error
	}
	other := make(chan otherResult, 1)
	go func() {
		active, err := cache.IsActiveParticipant(context.Background(), 6, "gonka1participant")
		if err == nil {
			_, err = cache.GetEpochGroupData(context.Background(), 5)
		}
		other <- otherResult{active, err}
	}()
	select {
	case res := <-other:
		require.NoError(t, res.err)
		require.True(t, res.active)
	case <-time.After(2 * time.Second):
		t.Fatal("lookups for other epochs blocked behind a hung query")
	}

	close(q.release)
	<-hung
}

func TestEpochCache_ConcurrentMissesMakeOneQuery(t *testing.T) {
	q := newBlockingEpochQueryClient(7)
	q.result = types.EpochGroupData{}
	cache := newBlockingEpochCache(q)

	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := cache.GetEpochGroupData(context.Background(), 7)
			errs <- err
		}()
	}
	waitEntered(t, q)
	time.Sleep(50 * time.Millisecond)
	close(q.release)
	for i := 0; i < 8; i++ {
		require.NoError(t, <-errs)
	}

	require.EqualValues(t, 1, q.calls.Load())
}

func TestEpochCache_CurrentConcurrentMissesMakeOneQuery(t *testing.T) {
	q := newBlockingEpochQueryClient(0)
	cache := newBlockingEpochCache(q)

	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := cache.GetCurrentEpochGroupData(7)
			errs <- err
		}()
	}
	waitEntered(t, q)
	time.Sleep(50 * time.Millisecond)
	close(q.release)
	for i := 0; i < 8; i++ {
		require.NoError(t, <-errs)
	}

	require.EqualValues(t, 1, q.calls.Load())
	require.True(t, q.sawDeadline.Load())
}

func TestEpochCache_CanceledCallerDoesNotFailOtherWaiters(t *testing.T) {
	q := newBlockingEpochQueryClient(7)
	cache := newBlockingEpochCache(q)

	ctx, cancel := context.WithCancel(context.Background())
	canceled := make(chan error, 1)
	go func() {
		_, err := cache.GetEpochGroupData(ctx, 7)
		canceled <- err
	}()
	waitEntered(t, q)

	waiter := make(chan error, 1)
	go func() {
		_, err := cache.GetEpochGroupData(context.Background(), 7)
		waiter <- err
	}()
	time.Sleep(50 * time.Millisecond)

	cancel()
	require.ErrorIs(t, <-canceled, context.Canceled)
	require.False(t, q.sawCancelled.Load())

	close(q.release)
	require.NoError(t, <-waiter)
	require.True(t, q.sawDeadline.Load())
}

func TestEpochCache_FailureIsNotCached(t *testing.T) {
	q := newBlockingEpochQueryClient(0)
	q.err = errors.New("node down")
	cache := newBlockingEpochCache(q)

	_, err := cache.GetEpochGroupData(context.Background(), 7)
	require.Error(t, err)

	q.err = nil
	data, err := cache.GetEpochGroupData(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, data.ValidationWeights, 1)
	require.EqualValues(t, 2, q.calls.Load())
}

func TestEpochCache_CurrentDoesNotMoveBackToOlderEpoch(t *testing.T) {
	q := newBlockingEpochQueryClient(0)
	close(q.release)
	cache := newBlockingEpochCache(q)

	_, err := cache.GetCurrentEpochGroupData(8)
	require.NoError(t, err)
	_, err = cache.GetCurrentEpochGroupData(7)
	require.NoError(t, err)

	require.EqualValues(t, 8, cache.cachedEpochIndex)
}

func TestEpochCache_DeadlineHoldsWhenTransportIgnoresContext(t *testing.T) {
	q := newBlockingEpochQueryClient(7)
	q.ignoreCtx = true
	t.Cleanup(func() { close(q.release) })
	cache := newBlockingEpochCache(q)
	cache.queryTimeout = 50 * time.Millisecond

	_, err := cache.GetEpochGroupData(context.Background(), 7)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestEpochCache_CurrentDeadlineHoldsWhenTransportIgnoresContext(t *testing.T) {
	q := newBlockingEpochQueryClient(0)
	q.ignoreCtx = true
	t.Cleanup(func() { close(q.release) })
	cache := newBlockingEpochCache(q)
	cache.queryTimeout = 50 * time.Millisecond

	_, err := cache.GetCurrentEpochGroupData(7)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestEpochCache_QueriesAcrossEpochsAreBounded(t *testing.T) {
	q := newBlockingEpochQueryClient(0)
	cache := newBlockingEpochCache(q)

	_, err := cache.GetEpochGroupData(context.Background(), 100)
	require.NoError(t, err)

	q.calls.Store(0)
	q.blockAll = true
	q.ignoreCtx = true
	cache.queryTimeout = 100 * time.Millisecond

	errs := make(chan error, 32)
	for epoch := uint64(1); epoch <= 32; epoch++ {
		go func(epoch uint64) {
			_, err := cache.GetEpochGroupData(context.Background(), epoch)
			errs <- err
		}(epoch)
	}
	for i := 0; i < 32; i++ {
		require.ErrorIs(t, <-errs, context.DeadlineExceeded)
	}
	require.EqualValues(t, maxConcurrentEpochGroupQueries, q.calls.Load())

	_, err = cache.GetEpochGroupData(context.Background(), 100)
	require.NoError(t, err)

	close(q.release)
	require.Eventually(t, func() bool {
		_, err := cache.GetEpochGroupData(context.Background(), 101)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)
}

func TestEpochCache_CanceledCallerStartsNoQuery(t *testing.T) {
	q := newBlockingEpochQueryClient(0)
	cache := newBlockingEpochCache(q)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := cache.GetEpochGroupData(ctx, 7)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, q.calls.Load())
}

func TestEpochCache_KeepsNewestEpochsUpToCapacity(t *testing.T) {
	q := newBlockingEpochQueryClient(0)
	close(q.release)
	q.blockEpoch = 1000
	cache := newBlockingEpochCache(q)

	for epoch := uint64(10); epoch >= 1; epoch-- {
		_, err := cache.GetEpochGroupData(context.Background(), epoch)
		require.NoError(t, err)
	}
	_, err := cache.GetEpochGroupData(context.Background(), 0)
	require.NoError(t, err)

	require.Len(t, cache.epochCache, maxCachedEpochs)
	for epoch := uint64(10); epoch > 10-maxCachedEpochs; epoch-- {
		require.Contains(t, cache.epochCache, epoch)
	}
}

func TestEpochCache_RetainedHistoricalEpochIsNotRequeried(t *testing.T) {
	q := newBlockingEpochQueryClient(1000)
	cache := newBlockingEpochCache(q)

	for _, epoch := range []uint64{10, 9, 3, 3, 3} {
		_, err := cache.GetEpochGroupData(context.Background(), epoch)
		require.NoError(t, err)
	}

	require.EqualValues(t, 3, q.calls.Load())
}

func TestIsActiveParticipant_AnswersForEpochNotRetained(t *testing.T) {
	q := newBlockingEpochQueryClient(1000)
	cache := newBlockingEpochCache(q)

	for epoch := uint64(20); epoch > 20-maxCachedEpochs; epoch-- {
		_, err := cache.GetEpochGroupData(context.Background(), epoch)
		require.NoError(t, err)
	}

	active, err := cache.IsActiveParticipant(context.Background(), 3, "gonka1participant")
	require.NoError(t, err)
	require.True(t, active)
	require.NotContains(t, cache.epochCache, uint64(3))
}
