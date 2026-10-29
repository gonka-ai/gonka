package session

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/nmrpc"
	"common/nodemanager/gen"
	"devshard/chainoracle/blocks/headertime"
	"devshard/hostevents"
	"devshard/internal/testutil"
	"devshard/state"
	"devshard/stub"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func unsetHeightSyncSources(t *testing.T) {
	t.Helper()
	t.Setenv("DEVSHARD_CHAIN_RPC", "")
	t.Setenv("NODE_RPC_URL", "")
	t.Setenv("DEVSHARD_COMET_RPC", "")
	t.Setenv("NODE_MANAGER_ADDR", "")
}

func newHeightSyncMgr(t *testing.T) *HostManager {
	t.Helper()
	return NewHostManager(newManagerTestStore(t), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)
}

type heightSyncNMServer struct {
	gen.UnimplementedNodeManagerServer
	oracle blocks.BlockOracle
}

func (s *heightSyncNMServer) GetBlockHeader(ctx context.Context, req *gen.GetBlockHeaderRequest) (*gen.GetBlockHeaderResponse, error) {
	return nmrpc.GetBlockHeader(ctx, s.oracle, req)
}

func (s *heightSyncNMServer) ProveBlockPath(ctx context.Context, req *gen.ProveBlockPathRequest) (*gen.ProveBlockPathResponse, error) {
	return nmrpc.ProveBlockPath(ctx, s.oracle, req)
}

type staticHeaderOracle struct {
	hdr *blocks.Header
}

func (s staticHeaderOracle) Latest(context.Context) (*blocks.Header, error) {
	if s.hdr == nil {
		return nil, blocks.ErrHeaderNotFound
	}
	cp := *s.hdr
	cp.BlockHash = append([]byte(nil), s.hdr.BlockHash...)
	return &cp, nil
}

func (s staticHeaderOracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	if s.hdr == nil || s.hdr.Height != height {
		return nil, blocks.ErrHeaderNotFound
	}
	return s.Latest(ctx)
}

func (staticHeaderOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (staticHeaderOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func dialNM(t *testing.T, srv gen.NodeManagerServer) gen.NodeManagerClient {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	gs := grpc.NewServer()
	gen.RegisterNodeManagerServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return gen.NewNodeManagerClient(conn)
}

func unimplementedNM(t *testing.T) gen.NodeManagerClient {
	t.Helper()
	return dialNM(t, &gen.UnimplementedNodeManagerServer{})
}

func TestHostManager_SetHostEventPoller(t *testing.T) {
	mgr := newHeightSyncMgr(t)
	require.Nil(t, mgr.HostEventPoller())
	p := hostevents.NewPoller(hostevents.PollerConfig{})
	mgr.SetHostEventPoller(p)
	require.True(t, mgr.HostEventPoller() == p)
}

func TestSetHeightSyncFromEnv_SharesOneHeaderTimeSource(t *testing.T) {
	unsetHeightSyncSources(t)
	nmHdr := blocks.HashOnlyHeader(12, time.Unix(1_700_000_000, 0).UTC(), "gonka-test", []byte{9, 9, 9, 9})
	nm := dialNM(t, &heightSyncNMServer{oracle: staticHeaderOracle{hdr: nmHdr}})
	mgr := newHeightSyncMgr(t)
	require.NoError(t, mgr.SetHeightSyncFromEnv(context.Background(), nil, nm))
	require.NotNil(t, mgr.headerTimes)

	got := state.AppliedHeaderTimes(mgr.sessionSMOpts()...)
	require.True(t, got == mgr.headerTimes, "session state machines read the manager's one map")

	at := time.Unix(1_700_000_001, 0).UTC()
	hash := []byte{1, 2, 3, 4}
	mgr.ObserveChainHeader(blocks.HashOnlyHeader(7, at, "gonka-test", hash))
	ms, err := mgr.headerTimes.HeaderTimeMs(7, hash)
	require.NoError(t, err)
	require.Equal(t, at.UnixMilli(), ms)
	rec, ok := mgr.headerTimes.StoredAt(7)
	require.True(t, ok)
	require.Equal(t, headertime.OriginComet, rec.Origin)
	require.True(t, rec.Verified)

	ms, err = got.HeaderTimeMs(7, hash)
	require.NoError(t, err)
	require.Equal(t, at.UnixMilli(), ms)
	mgr.CloseHeightSync()
}

func TestSeedHeaderTimesStoresLatestBeforeComet(t *testing.T) {
	unsetHeightSyncSources(t)
	at := time.Unix(1_700_000_000, 0).UTC()
	nmHdr := blocks.HashOnlyHeader(1000, at, "gonka-test", []byte{9, 9, 9, 9})
	nm := dialNM(t, &heightSyncNMServer{oracle: staticHeaderOracle{hdr: nmHdr}})
	mgr := newHeightSyncMgr(t)
	require.NoError(t, mgr.SetHeightSyncFromEnv(context.Background(), nil, nm))
	_, ok := mgr.headerTimes.StoredAt(1000)
	require.False(t, ok)

	mgr.SeedHeaderTimes(context.Background())
	rec, ok := mgr.headerTimes.StoredAt(1000)
	require.True(t, ok)
	require.Equal(t, headertime.OriginChain, rec.Origin)
	require.False(t, rec.Verified)
	ms, err := mgr.headerTimes.HeaderTimeMs(1000, []byte{9, 9, 9, 9})
	require.NoError(t, err)
	require.Equal(t, at.UnixMilli(), ms)
	mgr.CloseHeightSync()
}

func TestSetHeightSyncFromEnv_EmptyIsNoop(t *testing.T) {
	unsetHeightSyncSources(t)
	mgr := newHeightSyncMgr(t)
	require.NoError(t, mgr.SetHeightSyncFromEnv(context.Background(), nil, nil))
	require.Nil(t, mgr.heightSync)
	require.Nil(t, mgr.chainOracle)
	require.Len(t, mgr.transportServerOpts(), 3)
}

func TestSetHeightSyncFromEnv_InvalidK(t *testing.T) {
	unsetHeightSyncSources(t)
	t.Setenv("DEVSHARD_HEIGHTSYNC_K", "nope")
	mgr := newHeightSyncMgr(t)
	err := mgr.SetHeightSyncFromEnv(context.Background(), nil, unimplementedNM(t))
	require.Error(t, err)
	require.Nil(t, mgr.heightSync)
}

func TestSetHeightSyncFromEnv_WiresGetBlockHeader(t *testing.T) {
	unsetHeightSyncSources(t)
	t.Setenv("DEVSHARD_HEIGHTSYNC_K", "10")
	t.Setenv("DEVSHARD_HEIGHTSYNC_SLOTS", "1")

	nmHdr := blocks.HashOnlyHeader(12, time.Unix(1_700_000_000, 0).UTC(), "gonka-test", []byte{9, 9, 9, 9})
	nm := dialNM(t, &heightSyncNMServer{oracle: staticHeaderOracle{hdr: nmHdr}})
	mgr := newHeightSyncMgr(t)
	require.NoError(t, mgr.SetHeightSyncFromEnv(context.Background(), nil, nm))
	require.NotNil(t, mgr.heightSync)
	require.NotNil(t, mgr.chainOracle)
	require.Equal(t, uint64(10), mgr.heightSync.K())
	require.Equal(t, uint64(1), mgr.heightSync.SlotsNum())
	require.Len(t, mgr.transportServerOpts(), 4)

	hdr, err := mgr.chainOracle.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(12), hdr.Height, "empty cache falls through to GetBlockHeader")
	require.Equal(t, []byte{9, 9, 9, 9}, hdr.BlockHash)

	mgr.ObserveChainHeader(blocks.HashOnlyHeader(7, time.Unix(1_700_000_001, 0).UTC(), "gonka-test", []byte{1, 2, 3, 4}))
	hdr, err = mgr.chainOracle.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), hdr.Height, "Comet Observe is Latest primary")
	require.Equal(t, []byte{1, 2, 3, 4}, hdr.BlockHash)

	at, err := mgr.chainOracle.At(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(7), at.Height, "Observe still fills the At window")

	mgr.SetCometConnected(false)
	hdr, err = mgr.chainOracle.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(12), hdr.Height, "disconnected Comet must fall through to GetBlockHeader")
	mgr.CloseHeightSync()
	require.Nil(t, mgr.heightSync)
}

func TestSetHeightSyncFromEnv_UnimplementedFallsBackToCometTip(t *testing.T) {
	unsetHeightSyncSources(t)
	mgr := newHeightSyncMgr(t)
	require.NoError(t, mgr.SetHeightSyncFromEnv(context.Background(), nil, unimplementedNM(t)))
	require.NotNil(t, mgr.heightSync)

	mgr.ObserveChainHeader(blocks.HashOnlyHeader(7, time.Unix(1_700_000_000, 0).UTC(), "gonka-test", []byte{1, 2, 3, 4}))
	hdr, err := mgr.chainOracle.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), hdr.Height)
	mgr.CloseHeightSync()
}

func TestSetHeightSyncFromEnv_ChainRPCAloneIsNoop(t *testing.T) {
	unsetHeightSyncSources(t)
	t.Setenv("NODE_RPC_URL", "http://127.0.0.1:26657")
	mgr := newHeightSyncMgr(t)
	require.NoError(t, mgr.SetHeightSyncFromEnv(context.Background(), nil, nil))
	require.Nil(t, mgr.heightSync)
	require.Nil(t, mgr.chainOracle)
}

func TestSetHeightSyncFromEnv_NodeManagerWires(t *testing.T) {
	unsetHeightSyncSources(t)
	mgr := newHeightSyncMgr(t)
	require.NoError(t, mgr.SetHeightSyncFromEnv(context.Background(), nil, unimplementedNM(t)))
	require.NotNil(t, mgr.heightSync)
	require.NotNil(t, mgr.chainOracle)
	mgr.CloseHeightSync()
	require.Nil(t, mgr.heightSync)
	require.NotNil(t, mgr.chainOracle, "CloseHeightSync keeps the oracle for in-flight Observe")
}

func TestObserveChainHeader_AfterCloseStillRecordsTip(t *testing.T) {
	unsetHeightSyncSources(t)
	nmHdr := blocks.HashOnlyHeader(12, time.Unix(1_700_000_000, 0).UTC(), "gonka-test", []byte{9, 9, 9, 9})
	nm := dialNM(t, &heightSyncNMServer{oracle: staticHeaderOracle{hdr: nmHdr}})
	mgr := newHeightSyncMgr(t)
	require.NoError(t, mgr.SetHeightSyncFromEnv(context.Background(), nil, nm))

	mgr.CloseHeightSync()
	require.Nil(t, mgr.heightSync)
	require.NotNil(t, mgr.chainOracle)

	mgr.ObserveChainHeader(blocks.HashOnlyHeader(7, time.Unix(1_700_000_001, 0).UTC(), "gonka-test", []byte{1, 2, 3, 4}))
	hdr, err := mgr.chainOracle.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), hdr.Height, "Observe after CloseHeightSync still fills the Comet tip")

	mgr.SetCometConnected(false)
	hdr, err = mgr.chainOracle.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(12), hdr.Height)
}

func TestObserveChainHeader_ConcurrentCloseHeightSync(t *testing.T) {
	unsetHeightSyncSources(t)
	nmHdr := blocks.HashOnlyHeader(12, time.Unix(1_700_000_000, 0).UTC(), "gonka-test", []byte{9, 9, 9, 9})
	nm := dialNM(t, &heightSyncNMServer{oracle: staticHeaderOracle{hdr: nmHdr}})
	mgr := newHeightSyncMgr(t)
	require.NoError(t, mgr.SetHeightSyncFromEnv(context.Background(), nil, nm))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h := blocks.HashOnlyHeader(7, time.Unix(1_700_000_001, 0).UTC(), "gonka-test", []byte{1, 2, 3, 4})
		for {
			select {
			case <-stop:
				return
			default:
				mgr.ObserveChainHeader(h)
				mgr.SetCometConnected(false)
				mgr.SetCometConnected(true)
			}
		}
	}()
	for i := 0; i < 200; i++ {
		mgr.CloseHeightSync()
	}
	close(stop)
	wg.Wait()
}
