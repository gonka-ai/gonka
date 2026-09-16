package nmrpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/nodemanager/gen"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const testTip = int64(200_000)

func TestGetBlockHeaders_LPA2a_ThousandHeaderBatch(t *testing.T) {
	o := rangeOracle(testTip, testTip-1999, testTip-1000)
	resp, err := GetBlockHeaders(context.Background(), o, &gen.GetBlockHeadersRequest{
		FromHeight: testTip - 2000,
	})
	require.NoError(t, err)
	require.False(t, resp.Unchanged)
	require.Len(t, resp.Headers, blocks.MaxHeadersPerPoll)
	require.Equal(t, testTip-1999, resp.Headers[0].Height)
	require.Equal(t, testTip-1000, resp.Headers[len(resp.Headers)-1].Height)
	require.Equal(t, testTip-1000, resp.NextFromHeight)
	require.Equal(t, blocks.OldestHeight(testTip), resp.OldestHeight)
	require.Equal(t, testTip, resp.TipHeight)
}

func TestGetBlockHeaders_LPA2b_FollowUpIsContiguous(t *testing.T) {
	o := rangeOracle(testTip, testTip-1999, testTip)
	first, err := GetBlockHeaders(context.Background(), o, &gen.GetBlockHeadersRequest{
		FromHeight: testTip - 2000,
	})
	require.NoError(t, err)
	require.Equal(t, testTip-1000, first.NextFromHeight)

	second, err := GetBlockHeaders(context.Background(), o, &gen.GetBlockHeadersRequest{
		FromHeight: first.NextFromHeight,
	})
	require.NoError(t, err)
	require.Len(t, second.Headers, blocks.MaxHeadersPerPoll)
	require.Equal(t, first.NextFromHeight+1, second.Headers[0].Height)
	require.Equal(t, testTip, second.Headers[len(second.Headers)-1].Height)
	require.Equal(t, testTip, second.NextFromHeight)
}

func TestGetBlockHeaders_LPA2c_FromBelowWindowStartsAtOldest(t *testing.T) {
	oldest := blocks.OldestHeight(testTip)
	o := rangeOracle(testTip, oldest, oldest+int64(blocks.MaxHeadersPerPoll)-1)
	resp, err := GetBlockHeaders(context.Background(), o, &gen.GetBlockHeadersRequest{
		FromHeight: testTip - blocks.HistoryWindow - 1,
	})
	require.NoError(t, err)
	require.Equal(t, oldest, resp.Headers[0].Height)
	require.Equal(t, oldest+int64(blocks.MaxHeadersPerPoll)-1, resp.NextFromHeight)
}

func TestGetBlockHeaders_LPA2d_FromZeroStartsAtOldest(t *testing.T) {
	oldest := blocks.OldestHeight(testTip)
	o := rangeOracle(testTip, oldest, oldest+int64(blocks.MaxHeadersPerPoll)-1)
	resp, err := GetBlockHeaders(context.Background(), o, &gen.GetBlockHeadersRequest{FromHeight: 0})
	require.NoError(t, err)
	require.Equal(t, oldest, resp.Headers[0].Height)
	require.NotEqual(t, int64(1), resp.Headers[0].Height)
}

func TestGetBlockHeaders_LPA2e_MaxHeadersClampedTo1000(t *testing.T) {
	o := rangeOracle(testTip, testTip-1999, testTip-1000)
	for _, max := range []uint32{0, 5000} {
		resp, err := GetBlockHeaders(context.Background(), o, &gen.GetBlockHeadersRequest{
			FromHeight: testTip - 2000,
			MaxHeaders: max,
		})
		require.NoError(t, err, "max_headers=%d", max)
		require.Len(t, resp.Headers, blocks.MaxHeadersPerPoll)
	}
}

func TestGetBlockHeaders_LPA2f_CaughtUpImmediateUnchanged(t *testing.T) {
	o := rangeOracle(testTip, testTip, testTip)
	resp, err := GetBlockHeaders(context.Background(), o, &gen.GetBlockHeadersRequest{
		FromHeight:     testTip,
		MaxWaitSeconds: 0,
	})
	require.NoError(t, err)
	require.True(t, resp.Unchanged)
	require.Empty(t, resp.Headers)
	require.Equal(t, testTip, resp.NextFromHeight)
}

func TestGetBlockHeaders_LPA2g_LongPollWakesOnObserve(t *testing.T) {
	o := newLiveOracle(hdr(testTip))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan *gen.GetBlockHeadersResponse, 1)
	go func() {
		resp, err := GetBlockHeaders(ctx, o, &gen.GetBlockHeadersRequest{
			FromHeight:     testTip,
			MaxWaitSeconds: 5,
		})
		require.NoError(t, err)
		done <- resp
	}()

	require.Eventually(t, func() bool { return o.subscribers() > 0 }, time.Second, 5*time.Millisecond)
	o.Observe(hdr(testTip + 1))

	select {
	case resp := <-done:
		require.False(t, resp.Unchanged)
		require.Len(t, resp.Headers, 1)
		require.Equal(t, testTip+1, resp.Headers[0].Height)
		require.Equal(t, testTip+1, resp.NextFromHeight)
	case <-ctx.Done():
		t.Fatal("long-poll did not wake on Observe")
	}
}

func TestGetBlockHeaders_LPA2h_TimeoutUnchangedCursor(t *testing.T) {
	o := newLiveOracle(hdr(testTip))
	start := time.Now()
	resp, err := GetBlockHeaders(context.Background(), o, &gen.GetBlockHeadersRequest{
		FromHeight:     testTip,
		MaxWaitSeconds: 1,
	})
	require.NoError(t, err)
	require.True(t, resp.Unchanged)
	require.Empty(t, resp.Headers)
	require.Equal(t, testTip, resp.NextFromHeight)
	require.GreaterOrEqual(t, time.Since(start), time.Second)
}

func TestGetBlockHeaders_LPA2i_SubscribeBeforeRead(t *testing.T) {
	inner := newLiveOracle(hdr(testTip))
	o := &gatedOracle{liveOracle: inner, atGate: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan *gen.GetBlockHeadersResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := GetBlockHeaders(ctx, o, &gen.GetBlockHeadersRequest{
			FromHeight:     testTip,
			MaxWaitSeconds: 5,
		})
		errCh <- err
		done <- resp
	}()

	require.Eventually(t, func() bool { return inner.subscribers() > 0 }, time.Second, 5*time.Millisecond)
	inner.Observe(hdr(testTip + 1))
	close(o.atGate)

	select {
	case err := <-errCh:
		require.NoError(t, err)
		resp := <-done
		require.False(t, resp.Unchanged)
		require.Equal(t, testTip+1, resp.Headers[0].Height)
	case <-ctx.Done():
		t.Fatal("Observe between Subscribe and At was lost")
	}
}

func TestGetBlockHeaders_LPA2j_HoleStopsBatch(t *testing.T) {
	at := map[int64]*blocks.Header{}
	// Contiguous until H-1501; H-1500 missing; later heights present.
	for h := testTip - 1999; h <= testTip-1501; h++ {
		at[h] = hdr(h)
	}
	for h := testTip - 1499; h <= testTip-1000; h++ {
		at[h] = hdr(h)
	}
	o := stubOracle{latest: hdr(testTip), at: at}
	resp, err := GetBlockHeaders(context.Background(), o, &gen.GetBlockHeadersRequest{
		FromHeight: testTip - 2000,
	})
	require.NoError(t, err)
	require.Equal(t, testTip-1999, resp.Headers[0].Height)
	require.Equal(t, testTip-1501, resp.Headers[len(resp.Headers)-1].Height)
	require.Equal(t, testTip-1501, resp.NextFromHeight)
	require.NotEqual(t, testTip-1000, resp.NextFromHeight)
}

func TestGetBlockHeaders_LPA2k_ExistingFieldNumbersUnchanged(t *testing.T) {
	assertFieldNum(t, (&gen.AcquireMLNodeRequest{}).ProtoReflect().Descriptor(), "model", 1)
	assertFieldNum(t, (&gen.AcquireMLNodeRequest{}).ProtoReflect().Descriptor(), "excluded_nodes", 2)
	assertFieldNum(t, (&gen.AcquireMLNodeRequest{}).ProtoReflect().Descriptor(), "escrow_id", 3)

	assertFieldNum(t, (&gen.GetRuntimeConfigRequest{}).ProtoReflect().Descriptor(), "client_params_block_height", 1)
	assertFieldNum(t, (&gen.GetRuntimeConfigRequest{}).ProtoReflect().Descriptor(), "max_wait_seconds", 2)

	assertFieldNum(t, (&gen.GetHostEventsRequest{}).ProtoReflect().Descriptor(), "cursor", 1)
	assertFieldNum(t, (&gen.GetHostEventsRequest{}).ProtoReflect().Descriptor(), "max_wait_seconds", 2)

	assertFieldNum(t, (&gen.GetBlockHeaderRequest{}).ProtoReflect().Descriptor(), "height", 1)
	assertFieldNum(t, (&gen.GetBlockHeaderResponse{}).ProtoReflect().Descriptor(), "header", 1)
	assertFieldNum(t, (&gen.ProveBlockPathRequest{}).ProtoReflect().Descriptor(), "height", 1)
	assertFieldNum(t, (&gen.ProveBlockPathRequest{}).ProtoReflect().Descriptor(), "path", 2)

	assertFieldNum(t, (&gen.BlockHeader{}).ProtoReflect().Descriptor(), "height", 1)
	assertFieldNum(t, (&gen.BlockHeader{}).ProtoReflect().Descriptor(), "time_unix_nano", 2)
	assertFieldNum(t, (&gen.BlockHeader{}).ProtoReflect().Descriptor(), "chain_id", 3)
	assertFieldNum(t, (&gen.BlockHeader{}).ProtoReflect().Descriptor(), "block_hash", 4)

	assertFieldNum(t, (&gen.GetBlockHeadersRequest{}).ProtoReflect().Descriptor(), "from_height", 1)
	assertFieldNum(t, (&gen.GetBlockHeadersRequest{}).ProtoReflect().Descriptor(), "max_wait_seconds", 2)
	assertFieldNum(t, (&gen.GetBlockHeadersRequest{}).ProtoReflect().Descriptor(), "max_headers", 3)

	assertFieldNum(t, (&gen.GetBlockHeadersResponse{}).ProtoReflect().Descriptor(), "unchanged", 1)
	assertFieldNum(t, (&gen.GetBlockHeadersResponse{}).ProtoReflect().Descriptor(), "headers", 2)
	assertFieldNum(t, (&gen.GetBlockHeadersResponse{}).ProtoReflect().Descriptor(), "next_from_height", 3)
	assertFieldNum(t, (&gen.GetBlockHeadersResponse{}).ProtoReflect().Descriptor(), "oldest_height", 4)
	assertFieldNum(t, (&gen.GetBlockHeadersResponse{}).ProtoReflect().Descriptor(), "tip_height", 5)
}

func TestGetBlockHeaders_StatusCodes(t *testing.T) {
	_, err := GetBlockHeaders(context.Background(), nil, &gen.GetBlockHeadersRequest{})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))

	_, err = GetBlockHeaders(context.Background(), stubOracle{latest: hdr(1)}, &gen.GetBlockHeadersRequest{FromHeight: -1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func assertFieldNum(t *testing.T, md protoreflect.MessageDescriptor, name string, want protoreflect.FieldNumber) {
	t.Helper()
	fd := md.Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, fd, "missing field %s on %s", name, md.FullName())
	require.Equal(t, want, fd.Number(), "field %s on %s", name, md.FullName())
}

func rangeOracle(tip, from, to int64) stubOracle {
	at := make(map[int64]*blocks.Header, to-from+2)
	for h := from; h <= to; h++ {
		at[h] = hdr(h)
	}
	at[tip] = hdr(tip)
	return stubOracle{latest: hdr(tip), at: at}
}

func hdr(height int64) *blocks.Header {
	return blocks.HashOnlyHeader(height, time.Unix(height, 0).UTC(), "gonka", []byte{byte(height)})
}

type liveOracle struct {
	mu       sync.Mutex
	latest   *blocks.Header
	byHeight map[int64]*blocks.Header
	subs     []chan *blocks.Header
}

func newLiveOracle(tip *blocks.Header) *liveOracle {
	o := &liveOracle{byHeight: map[int64]*blocks.Header{}}
	if tip != nil {
		o.latest = tip
		o.byHeight[tip.Height] = tip
	}
	return o
}

func (o *liveOracle) Observe(h *blocks.Header) {
	if h == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.latest = h
	o.byHeight[h.Height] = h
	for _, ch := range o.subs {
		select {
		case ch <- h:
		default:
		}
	}
}

func (o *liveOracle) subscribers() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.subs)
}

func (o *liveOracle) Latest(context.Context) (*blocks.Header, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.latest == nil {
		return nil, blocks.ErrHeaderNotFound
	}
	return o.latest, nil
}

func (o *liveOracle) At(_ context.Context, height int64) (*blocks.Header, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	h, ok := o.byHeight[height]
	if !ok {
		return nil, blocks.ErrHeaderNotFound
	}
	return h, nil
}

func (o *liveOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (o *liveOracle) Subscribe(ctx context.Context, _ int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header, 16)
	o.mu.Lock()
	o.subs = append(o.subs, ch)
	o.mu.Unlock()
	go func() {
		<-ctx.Done()
	}()
	return ch, nil
}

type gatedOracle struct {
	*liveOracle
	atGate chan struct{}
}

func (o *gatedOracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	select {
	case <-o.atGate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return o.liveOracle.At(ctx, height)
}
