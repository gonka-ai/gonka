package user

import (
	"context"
	"errors"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"devshard/bridge"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

type bootstrapGrantLog struct {
	height int64
	closed bool
}

func (b *bootstrapGrantLog) FetchWarmKeyLog(ctx context.Context, cold string) ([]bridge.WarmKeyEntry, error) {
	entries, _, err := b.FetchWarmKeyLogWithHeight(ctx, cold)
	return entries, err
}
func (b *bootstrapGrantLog) FetchWarmKeyLogWithHeight(context.Context, string) ([]bridge.WarmKeyEntry, int64, error) {
	if b.closed {
		return nil, 0, errors.New("direct grant query disabled")
	}
	return nil, b.height, nil
}
func (b *bootstrapGrantLog) FetchWarmKeyEntry(context.Context, string, uint64) (bridge.WarmKeyEntry, bool, error) {
	return bridge.WarmKeyEntry{}, false, errors.New("direct grant entry query disabled")
}

func TestV7FirstWarmReceiptAndBindShareDiffWithOfflineGrantQueries(t *testing.T) {
	ctx := context.Background()
	owner, cold, warm := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	addr, err := blocks.AddressBytes(cold.CompressedPublicKeyBytes())
	require.NoError(t, err)
	oracle, err := mockoracle.NewMock(mockoracle.MockConfig{
		LightBlocks: true, ChainID: "first-warm-receipt", InitialHeight: 100,
		Validators: []mockoracle.MockValidator{{Signer: cold, Address: addr, Power: 10}},
		Start:      time.Now().Add(-10 * time.Second).UTC(), BlockInterval: time.Second,
	})
	require.NoError(t, err)
	anchor, err := oracle.AdvanceOne()
	require.NoError(t, err)
	fetch := &bootstrapGrantLog{height: anchor.Height}
	gatewayGrants, hostGrants := bridge.NewWarmKeyLogCopy(fetch, time.Hour), bridge.NewWarmKeyLogCopy(fetch, time.Hour)
	for _, grants := range []*bridge.WarmKeyLogCopy{gatewayGrants, hostGrants} {
		_, err = grants.WarmKeyLogAtHeight(cold.Address(), anchor.Height)
		require.NoError(t, err)
		grants.ApplyWarmKeyFeedPage(1, []bridge.WarmKeyFeedEvent{{Height: anchor.Height}})
	}
	fetch.closed = true

	cfg := testutil.DefaultConfig(1)
	cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, anchor.BlockHash
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{cold})
	const escrow = "first-warm-receipt"
	const balance = 1_000_000
	verifier := signing.NewSecp256k1Verifier()
	makeSM := func(grants *bridge.WarmKeyLogCopy) *state.StateMachine {
		sm, err := state.NewStateMachine(escrow, cfg, group, balance, owner.Address(), verifier,
			testutil.MustMemoryStore(t, escrow, owner.Address(), cfg, group, balance),
			state.WithVersion("v7"), state.WithMainnetHeights(oracle),
			state.WithWarmKeyHeightChecker(func(cold, warm string, id uint64, height int64, at time.Time) (bool, error) {
				return bridge.CheckWarmKeyAtHeight(grants, cold, warm, id, height, at)
			}))
		require.NoError(t, err)
		return sm
	}
	gatewaySM, hostSM := makeSM(gatewayGrants), makeSM(hostGrants)
	gateway, err := NewSession(gatewaySM, owner, escrow, group, make([]HostClient, 1), verifier, WithWarmKeyGrantSource(gatewayGrants))
	require.NoError(t, err)
	defer gateway.Close()
	receiver, err := host.NewHost(hostSM, cold, stub.NewInferenceEngine(), escrow, group, nil, host.WithChainOracle(oracle))
	require.NoError(t, err)

	start := testutil.StartTxVersioned(1, "v7")
	initial, _, err := gateway.composeDiffLocked(ctx, []*types.DevshardTx{start})
	require.NoError(t, err)
	require.Empty(t, gatewaySM.WarmKeyHistory())
	_, err = receiver.HandleRequest(ctx, host.HostRequest{Diffs: []types.Diff{initial}, Nonce: 1})
	require.NoError(t, err)

	atH, err := oracle.AdvanceOne()
	require.NoError(t, err)
	entry := bridge.WarmKeyEntry{
		Granter: cold.Address(), Grantee: warm.Address(), ID: 1, GrantedAtHeight: atH.Height,
		ExpiresAtUnix: atH.Time.Add(time.Second).UnixNano(), // inclusive at the receiver tip, even though wall time has passed it
	}
	for _, grants := range []*bridge.WarmKeyLogCopy{gatewayGrants, hostGrants} {
		grants.ApplyWarmKeyFeedPage(1, []bridge.WarmKeyFeedEvent{{Height: atH.Height, Entry: &entry}, {Height: atH.Height}})
	}

	// This is the key's first signature. The initial diff predates the grant.
	msg := start.GetStartInference()
	receipt := testutil.SignExecutorReceipt(t, warm, escrow, msg.InferenceId, msg.PromptHash, msg.Model, msg.InputLength, msg.MaxTokens)
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: msg.InferenceId, ExecutorSig: receipt,
	}}}
	gateway.addPendingFromHostLocked(0, nil, confirm)
	diff, _, err := gateway.composeDiffLocked(ctx, nil)
	require.NoError(t, err)
	require.Len(t, diff.Txs, 3)
	require.Equal(t, atH.Height, diff.Txs[0].GetMainnetHeight().Height)
	require.Equal(t, warm.Address(), diff.Txs[1].GetWarmKeyBind().Address)
	require.Equal(t, receipt, diff.Txs[2].GetConfirmStart().ExecutorSig)

	atNext, err := oracle.AdvanceOne()
	require.NoError(t, err)
	require.Equal(t, atH.Height+1, atNext.Height)
	hostGrants.ApplyWarmKeyFeedPage(1, []bridge.WarmKeyFeedEvent{{Height: atNext.Height}})
	resp, err := receiver.HandleRequest(ctx, host.HostRequest{Diffs: []types.Diff{diff}, Nonce: 2})
	require.NoError(t, err)
	require.NotEmpty(t, resp.StateSig)
	require.Equal(t, diff.PostStateRoot, resp.StateHash)
	require.Equal(t, gatewaySM.MainnetCheckpoint(), hostSM.MainnetCheckpoint())
	record, ok := hostSM.Inference(msg.InferenceId)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, record.Status)
}
