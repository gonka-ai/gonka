package event_listener

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/tipcache"
	"common/nodemanager/gen"
	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/nodemanager"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	lpa5Time    = "2024-01-02T03:04:05.123456789Z"
	lpa5ChainID = "gonka-test"
	lpa5HashHex = "ABCDEF123456"
)

func newBlockJSONRPC(height int64, hashHex, chainID, rfc3339 string) *chainevents.JSONRPCResponse {
	return &chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Data: chainevents.Data{
				Type: newBlockEventType,
				Value: map[string]interface{}{
					"block": map[string]interface{}{
						"header": map[string]interface{}{
							"height":   strconv.FormatInt(height, 10),
							"time":     rfc3339,
							"chain_id": chainID,
						},
					},
					"block_id": map[string]interface{}{
						"hash": hashHex,
					},
				},
			},
		},
	}
}

func observeInto(cache *tipcache.Cache) func(chainphase.BlockInfo) {
	return func(info chainphase.BlockInfo) {
		hash, err := decodeObserveHash(info.Hash)
		if err != nil {
			return
		}
		cache.Observe(blocks.HashOnlyHeader(info.Height, info.Time, info.ChainID, hash))
	}
}

func TestParseNewBlockInfo_LPA5a(t *testing.T) {
	event := newBlockJSONRPC(12345, lpa5HashHex, lpa5ChainID, lpa5Time)
	info, err := parseNewBlockInfo(event)
	require.NoError(t, err)
	require.Equal(t, int64(12345), info.Height)
	require.Equal(t, lpa5HashHex, info.Hash)
	require.Equal(t, lpa5ChainID, info.ChainID)
	wantTime, err := time.Parse(time.RFC3339Nano, lpa5Time)
	require.NoError(t, err)
	require.True(t, info.Time.Equal(wantTime.UTC()))

	hash, err := decodeObserveHash(info.Hash)
	require.NoError(t, err)
	wantHash, err := hex.DecodeString(lpa5HashHex)
	require.NoError(t, err)
	require.Equal(t, wantHash, hash)

	prefixed, err := decodeObserveHash("0x" + lpa5HashHex)
	require.NoError(t, err)
	require.Equal(t, wantHash, prefixed)

	full, err := decodeObserveHash(strings.Repeat("ab", maxBlockHashBytes))
	require.NoError(t, err)
	require.Len(t, full, maxBlockHashBytes)

	_, err = decodeObserveHash(strings.Repeat("aa", maxBlockHashBytes+1))
	require.Error(t, err)
}

func TestProcessEvent_NonHexHashSkipsObserve(t *testing.T) {
	called := 0
	el := &EventListener{onNewBlockHeader: func(chainphase.BlockInfo) { called++ }}
	el.processEvent(newBlockJSONRPC(2, "not-hex", lpa5ChainID, lpa5Time), "t")
	require.Equal(t, 0, called)
}

func TestProcessEvent_OversizedHashSkipsObserve(t *testing.T) {
	called := 0
	el := &EventListener{onNewBlockHeader: func(chainphase.BlockInfo) { called++ }}
	el.processEvent(newBlockJSONRPC(1, strings.Repeat("aa", maxBlockHashBytes+1), lpa5ChainID, lpa5Time), "t")
	require.Equal(t, 0, called)
}

func TestProcessEvent_ValidHashObserves(t *testing.T) {
	var got chainphase.BlockInfo
	el := &EventListener{onNewBlockHeader: func(info chainphase.BlockInfo) { got = info }}
	el.processEvent(newBlockJSONRPC(55, lpa5HashHex, lpa5ChainID, lpa5Time), "t")
	require.Equal(t, int64(55), got.Height)
	require.Equal(t, lpa5HashHex, got.Hash)
	require.Equal(t, lpa5ChainID, got.ChainID)
}

func TestProcessEvent_LPA5b_ObserveMatchesGetBlockHeader(t *testing.T) {
	cache := tipcache.New(time.Hour)
	ring := apiconfig.NewHostEventRing(64, 1)
	el := &EventListener{onNewBlockHeader: observeInto(cache), hostEvents: ring}
	el.processEvent(newBlockJSONRPC(55, lpa5HashHex, lpa5ChainID, lpa5Time), "t")

	got, err := cache.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(55), got.Height)
	require.Equal(t, uint64(0), ring.Head(), "NewBlock must not append to HostEventRing")

	srv := nodemanager.NewServer(nil, nil, nil, nodemanager.WithBlockOracle(cache))
	resp, err := srv.GetBlockHeader(context.Background(), &gen.GetBlockHeaderRequest{Height: 0})
	require.NoError(t, err)
	require.Equal(t, int64(55), resp.Header.Height)
	require.Equal(t, got.BlockHash, resp.Header.BlockHash)
	require.Equal(t, lpa5ChainID, resp.Header.ChainId)
}

func TestProcessEvent_LPA5c_ParamsUnchangedStillObserves(t *testing.T) {
	qc := &mockParamsQueryClient{}
	dispatcher, cm := newRuntimeCacheTestDispatcher(t, qc)
	resp := devshardParamsResponse(true, 20000)
	qc.On("Params", mock.Anything, mock.Anything).Return(resp, nil).Twice()

	cache := tipcache.New(time.Hour)
	el := &EventListener{
		onNewBlockHeader: observeInto(cache),
		dispatcher:       dispatcher,
		configManager:    cm,
	}

	el.processEvent(newBlockJSONRPC(300, "aabbccdd", lpa5ChainID, lpa5Time), "t")
	require.Equal(t, int64(300), cm.RuntimeParamsBlockHeight())

	ch := cm.RuntimeConfigNotifier().NotifyChan()
	el.processEvent(newBlockJSONRPC(301, "11223344", lpa5ChainID, lpa5Time), "t")
	select {
	case <-ch:
		t.Fatal("expected no runtime-config notify when params and epoch unchanged")
	case <-time.After(50 * time.Millisecond):
	}
	require.Equal(t, int64(300), cm.RuntimeParamsBlockHeight())

	h, err := cache.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(301), h.Height)
	wantHash, err := hex.DecodeString("11223344")
	require.NoError(t, err)
	require.Equal(t, wantHash, h.BlockHash)
}

func TestProcessEvent_LPA5d_GetBlockHeadersWakesOnObserve(t *testing.T) {
	cache := tipcache.New(time.Hour)
	el := &EventListener{onNewBlockHeader: observeInto(cache)}
	el.processEvent(newBlockJSONRPC(10, "aabbccdd", lpa5ChainID, lpa5Time), "t")

	srv := nodemanager.NewServer(nil, nil, nil, nodemanager.WithBlockOracle(cache))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan *gen.GetBlockHeadersResponse, 1)
	go func() {
		resp, err := srv.GetBlockHeaders(ctx, &gen.GetBlockHeadersRequest{
			FromHeight:     10,
			MaxWaitSeconds: 5,
		})
		require.NoError(t, err)
		done <- resp
	}()

	time.Sleep(50 * time.Millisecond)
	el.processEvent(newBlockJSONRPC(11, "11223344", lpa5ChainID, lpa5Time), "t")

	select {
	case resp := <-done:
		require.False(t, resp.Unchanged)
		require.Len(t, resp.Headers, 1)
		require.Equal(t, int64(11), resp.Headers[0].Height)
		require.Equal(t, int64(11), resp.NextFromHeight)
	case <-ctx.Done():
		t.Fatal("long-poll did not wake on EventListener Observe")
	}
}

func TestNodeManager_LPA5e_NilOracleFailedPrecondition(t *testing.T) {
	srv := nodemanager.NewServer(nil, nil, nil)
	_, err := srv.GetBlockHeader(context.Background(), &gen.GetBlockHeaderRequest{Height: 0})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	_, err = srv.GetBlockHeaders(context.Background(), &gen.GetBlockHeadersRequest{})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}
