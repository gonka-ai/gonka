package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpcserver "github.com/cometbft/cometbft/rpc/jsonrpc/server"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type stalledHeightSyncNM struct {
	gen.UnimplementedNodeManagerServer
}

func (stalledHeightSyncNM) GetBlockHeader(ctx context.Context, _ *gen.GetBlockHeaderRequest) (*gen.GetBlockHeaderResponse, error) {
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

func TestHostHeightProofFallsBackToCometRPC(t *testing.T) {
	for _, tc := range []struct {
		name string
		nm   gen.NodeManagerServer
	}{
		{"unimplemented", &gen.UnimplementedNodeManagerServer{}},
		{"unavailable", unavailableHeightSyncNM{}},
		{"stalled", stalledHeightSyncNM{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsetHeightSyncSources(t)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()

			validator := mustGenerateKey(t)
			addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
			require.NoError(t, err)
			chain, err := mockoracle.NewMock(mockoracle.MockConfig{
				LightBlocks: true, ChainID: "direct-proof-test", InitialHeight: 100,
				Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}},
				Start:      time.Now().Add(-10 * time.Second).UTC(), BlockInterval: time.Second,
			})
			require.NoError(t, err)
			anchor, err := chain.AdvanceOne()
			require.NoError(t, err)
			atH, err := chain.AdvanceOne()
			require.NoError(t, err)
			atNext, err := chain.AdvanceOne()
			require.NoError(t, err)

			mux := http.NewServeMux()
			rpcserver.RegisterRPCFuncs(mux, map[string]*rpcserver.RPCFunc{
				"commit": rpcserver.NewRPCFunc(func(_ *rpctypes.Context, height int64) (*ctypes.ResultCommit, error) {
					raw, err := chain.LightBlockAt(ctx, height)
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
					raw, err := chain.LightBlockAt(ctx, height)
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
			require.NoError(t, receiver.SetHeightSyncFromEnv(ctx, nil, nm, rpc.URL))
			sequencer.ObserveChainHeader(atH)
			receiver.ObserveChainHeader(atNext)
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
			require.NotEmpty(t, proof.GetMainnetHeight().LightBlocks)
			start := testutil.StartTxVersioned(1, "v7")
			txs := []*types.DevshardTx{proof, start}
			root, err := seqSM.ApplyLocal(1, txs)
			require.NoError(t, err)

			sm := newMachine(receiver)
			h, err := host.NewHost(sm, validator, stub.NewInferenceEngine(), escrowID, group, nil, receiver.appendChainOracleOpt(nil)...)
			require.NoError(t, err)
			diff := testutil.SignDiffWithRoot(t, user, escrowID, 1, txs, root)
			// HeaderTimeMs deliberately returns pending on a slow first lookup.
			// A subsequent request can sign once the background lookup finishes.
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				resp, err := h.HandleRequest(ctx, host.HostRequest{Diffs: []types.Diff{diff}, Nonce: 1})
				require.NoError(c, err)
				require.Equal(c, root, resp.StateHash)
				require.NotEmpty(c, resp.StateSig)
			}, 4*time.Second, 50*time.Millisecond)
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
