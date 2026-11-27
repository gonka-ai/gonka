package session

import (
	"context"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"common/chain"
	"devshard/bridge"
	chainbridge "devshard/cmd/devshardd/bridge"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/stub"
	"devshard/testenv/mockchain/grpcface"
	"devshard/testenv/mockchain/seed"
)

// warmFloodBridge is the test bridge with the production warm-key lookup.
type warmFloodBridge struct {
	mockBridge
	warm *chainbridge.ChainBridge
}

func (b *warmFloodBridge) VerifyWarmKey(warm, validator string) (bool, error) {
	return b.warm.VerifyWarmKey(warm, validator)
}

func newWarmFloodManager(t *testing.T, escrowID string) (*bindRPC, *atomic.Int64, int) {
	t.Helper()
	srv, lis, err := grpcface.NewInProcessServer(grpcface.Deps{Store: seed.Defaults()})
	require.NoError(t, err)
	t.Cleanup(func() { srv.Stop(); _ = lis.Close() })
	queries := new(atomic.Int64)
	count := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if method == "/inference.inference.Query/GranteesByMessageType" {
			queries.Add(1)
		}
		return inv(ctx, method, req, reply, cc, opts...)
	}
	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(count))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	hosts := make([]*signing.Secp256k1Signer, 3)
	addrs := make([]string, len(hosts))
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
		addrs[i] = hosts[i].Address()
	}
	br := &warmFloodBridge{
		mockBridge: mockBridge{escrow: &bridge.EscrowInfo{
			EscrowID: escrowID, EpochID: 7, Amount: 100000,
			CreatorAddress: mustGenerateKey(t).Address(), Slots: addrs, TokenPrice: 1,
		}},
		warm: chainbridge.NewChainBridge(chain.NewFromConn(conn), nil),
	}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(newManagerTestStore(t), hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
	_, err = mgr.SessionServer(escrowID)
	require.NoError(t, err)
	return newBindRPC(t, mgr), queries, len(hosts)
}

// An outsider Attach with a fresh key on a live session's escrow reaches
// isWarmKeySender: one chain query per group validator, and the "no" is
// memoised. The negatives must stay bounded, so after a flood larger than
// the cache the earliest keys cost queries again.
func TestAttach_OutsiderKeysDoNotGrowWarmCacheUnbounded(t *testing.T) {
	t.Setenv("DEVSHARD_RPC_LIMITS", "off") // the 50/s Attach floor only slows the flood
	rpc, queries, slots := newWarmFloodManager(t, "9930")

	const n = 1500 // 4500 negatives on a 3-validator group
	outsiders := make([]*signing.Secp256k1Signer, n)
	for i := range outsiders {
		outsiders[i] = mustGenerateKey(t)
		_, err := rpc.attach("9930", outsiders[i])
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	}
	require.Equal(t, int64(n*slots), queries.Load(), "one chain query per group validator per fresh key")

	before := queries.Load()
	const replay = 200
	for i := 0; i < replay; i++ {
		_, _ = rpc.attach("9930", outsiders[i])
	}
	t.Logf("%d outsider Attaches -> %d queries; replay of the first %d -> %d", n, before, replay, queries.Load()-before)
	require.Equal(t, int64(replay*slots), queries.Load()-before,
		"the earliest negatives were evicted, not kept for the life of the process")
}
