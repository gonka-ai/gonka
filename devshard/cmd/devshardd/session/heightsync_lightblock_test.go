package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"common/chain"
	"common/chainoracle/blocks"
	"common/chainoracle/blocks/lightproof"
	"common/nodemanager/gen"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"

	"github.com/cometbft/cometbft/libs/log"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpcserver "github.com/cometbft/cometbft/rpc/jsonrpc/server"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type stalledHeightSyncNM struct {
	gen.UnimplementedNodeManagerServer
	proofAttempts atomic.Int32
}

func (s *stalledHeightSyncNM) GetBlockHeader(ctx context.Context, req *gen.GetBlockHeaderRequest) (*gen.GetBlockHeaderResponse, error) {
	if req.GetIncludeLightBlock() {
		s.proofAttempts.Add(1)
	}
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

type latestChainConn struct {
	grpc.ClientConnInterface
	header *blocks.Header
}

func (c latestChainConn) Invoke(_ context.Context, method string, _, reply any, _ ...grpc.CallOption) error {
	if method != "/cosmos.base.tendermint.v1beta1.Service/GetLatestBlock" {
		return status.Error(codes.Unimplemented, method)
	}
	*reply.(*cmtservice.GetLatestBlockResponse) = cmtservice.GetLatestBlockResponse{
		BlockId:  &cmtproto.BlockID{Hash: c.header.BlockHash},
		SdkBlock: &cmtservice.Block{Header: cmtservice.Header{Height: c.header.Height, Time: c.header.Time, ChainID: c.header.ChainID}},
	}
	return nil
}

func TestHostHeightProofFallsBackToCometRPC(t *testing.T) {
	for _, tc := range []struct {
		name         string
		nm           gen.NodeManagerServer
		coldReceiver bool
	}{
		{name: "unimplemented", nm: &gen.UnimplementedNodeManagerServer{}},
		{name: "unavailable", nm: unavailableHeightSyncNM{}},
		{name: "stalled", nm: &stalledHeightSyncNM{}},
		{name: "stalled_without_receiver_websocket_tip", nm: &stalledHeightSyncNM{}, coldReceiver: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsetHeightSyncSources(t)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()

			validator := mustGenerateKey(t)
			addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
			require.NoError(t, err)
			mockChain, err := mockoracle.NewMock(mockoracle.MockConfig{
				LightBlocks: true, ChainID: "direct-proof-test", InitialHeight: 100,
				Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}},
				Start:      time.Now().Add(-10 * time.Second).UTC(), BlockInterval: time.Second,
			})
			require.NoError(t, err)
			anchor, err := mockChain.AdvanceOne()
			require.NoError(t, err)
			atH, err := mockChain.AdvanceOne()
			require.NoError(t, err)
			atNext, err := mockChain.AdvanceOne()
			require.NoError(t, err)

			mux := http.NewServeMux()
			rpcserver.RegisterRPCFuncs(mux, map[string]*rpcserver.RPCFunc{
				"commit": rpcserver.NewRPCFunc(func(_ *rpctypes.Context, height int64) (*ctypes.ResultCommit, error) {
					raw, err := mockChain.LightBlockAt(ctx, height)
					if err != nil {
						return nil, err
					}
					lb, err := lightproof.Decode(raw)
					if err != nil {
						return nil, err
					}
					return &ctypes.ResultCommit{SignedHeader: *lb.SignedHeader, CanonicalCommit: true}, nil
				}, "height"),
				"validators": rpcserver.NewRPCFunc(func(_ *rpctypes.Context, height int64, page, perPage int) (*ctypes.ResultValidators, error) {
					raw, err := mockChain.LightBlockAt(ctx, height)
					if err != nil {
						return nil, err
					}
					lb, err := lightproof.Decode(raw)
					if err != nil {
						return nil, err
					}
					vals := lb.ValidatorSet.Validators
					return &ctypes.ResultValidators{BlockHeight: height, Validators: vals, Count: len(vals), Total: len(vals)}, nil
				}, "height,page,per_page"),
			}, log.NewNopLogger())
			rpc := httptest.NewServer(mux)
			t.Cleanup(rpc.Close)
			nm := dialNM(t, tc.nm)

			sequencer := newHeightSyncMgr(t)
			receiver := newHeightSyncMgr(t)
			require.NoError(t, sequencer.SetHeightSyncFromEnv(ctx, nil, nm, rpc.URL))
			var chainQuery *chain.Client
			if tc.coldReceiver {
				chainQuery = chain.NewFromConn(latestChainConn{header: atNext})
			}
			require.NoError(t, receiver.SetHeightSyncFromEnv(ctx, chainQuery, nm, rpc.URL))
			sequencer.ObserveChainHeader(atH)
			if !tc.coldReceiver {
				receiver.ObserveChainHeader(atNext)
			}
			cfg := testutil.DefaultConfig(1)
			cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, anchor.BlockHash
			user := mustGenerateKey(t)
			group := testutil.MakeGroup([]*signing.Secp256k1Signer{validator})
			const escrowID = "direct-proof-test"
			const balance = 1_000_000
			newMachine := func(mgr *HostManager) *state.StateMachine {
				sm, err := state.NewStateMachine(escrowID, cfg, group, balance, user.Address(),
					signing.NewSecp256k1Verifier(), testutil.MustMemoryStore(t, escrowID, user.Address(), cfg, group, balance),
					mgr.sessionSMOpts(state.WithVersion("v7"))...)
				require.NoError(t, err)
				return sm
			}
			seqSM := newMachine(sequencer)
			proof, err := seqSM.BuildMainnetHeightTx(ctx)
			require.NoError(t, err)
			require.Equal(t, atH.Height, proof.GetMainnetHeight().Height)
			require.GreaterOrEqual(t, len(proof.GetMainnetHeight().LightBlocks), 2)
			if stalled, ok := tc.nm.(*stalledHeightSyncNM); ok {
				require.Equal(t, int32(1), stalled.proofAttempts.Load(), "one stalled dAPI request per proof")
			}
			start := testutil.StartTxVersioned(1, "v7")
			txs := []*types.DevshardTx{proof, start}
			root, err := seqSM.ApplyLocal(1, txs)
			require.NoError(t, err)

			sm := newMachine(receiver)
			h, err := host.NewHost(sm, validator, stub.NewInferenceEngine(), escrowID, group, nil, receiver.appendChainOracleOpt(nil)...)
			require.NoError(t, err)
			diff := testutil.SignDiffWithRoot(t, user, escrowID, 1, txs, root)
			// A receiver without a websocket tip can use chain gRPC while
			// the dAPI call is still blocked.
			resp, err := h.HandleRequest(ctx, host.HostRequest{Diffs: []types.Diff{diff}, Nonce: 1})
			require.NoError(t, err)
			require.Equal(t, root, resp.StateHash)
			require.NotEmpty(t, resp.StateSig)
			require.Equal(t, atH.Height, sm.MainnetCheckpoint().Height)
		})
	}
}

type unavailableHeightSyncNM struct {
	gen.UnimplementedNodeManagerServer
}

func (unavailableHeightSyncNM) GetBlockHeader(context.Context, *gen.GetBlockHeaderRequest) (*gen.GetBlockHeaderResponse, error) {
	return nil, status.Error(codes.Unavailable, "dAPI unavailable")
}
