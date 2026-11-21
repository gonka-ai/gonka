package app

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authz "github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
)

func buildTrapDealerMsg(t *testing.T, chainID string, epochID uint64, creator string) *blstypes.MsgSubmitDealerPart {
	t.Helper()

	// f(x) = -2 + x evaluates to 0 at slot 1 (x = 2), while having valid
	// non-zero constant term and valid Schnorr knowledge proofs.
	var c0, c1 fr.Element
	c0.SetUint64(2)
	c0.Neg(&c0)
	c1.SetOne()

	commit0, proof0, err := blstypes.BuildCommitmentKnowledgeProof(chainID, epochID, creator, 0, &c0)
	require.NoError(t, err)
	commit1, proof1, err := blstypes.BuildCommitmentKnowledgeProof(chainID, epochID, creator, 1, &c1)
	require.NoError(t, err)

	share := make([]byte, blstypes.HonestEncryptedShareCiphertextLen)
	share[0] = 0x04
	return &blstypes.MsgSubmitDealerPart{
		Creator:          creator,
		EpochId:          epochID,
		Commitments:      [][]byte{commit0, commit1},
		CommitmentProofs: []blstypes.CommitmentKnowledgeProof{proof0, proof1},
		EncryptedSharesForParticipants: []blstypes.EncryptedSharesForParticipant{
			{EncryptedShares: [][]byte{share, share}},
		},
	}
}

func setupDealerAttemptAnte(t *testing.T) (blskeeper.Keeper, blstypes.MsgServer, sdk.Context, sdk.AccAddress, *mockAuthzGrantKeeper, BlsDealerAttemptDecorator) {
	t.Helper()

	cfg := sdk.GetConfig()
	cfg.SetBech32PrefixForAccount("gonka", "gonkapub")
	cfg.SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")
	cfg.SetBech32PrefixForConsensusNode("gonkavalcons", "gonkavalconspub")

	bk, ctx := keepertest.BlsKeeper(t)
	ctx = ctx.WithChainID("gonka-mainnet-1").WithBlockHeight(10).WithExecMode(sdk.ExecModeFinalize)
	dealer := sdk.AccAddress("dealer_participant_1")

	require.NoError(t, bk.SetEpochBLSData(ctx, blstypes.EpochBLSData{
		EpochId:                   1,
		ITotalSlots:               2,
		TSlotsDegree:              1,
		DkgPhase:                  blstypes.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock: 15,
		Participants: []blstypes.BLSParticipantInfo{
			{
				Address:          dealer.String(),
				PercentageWeight: math.LegacyNewDec(100),
				SlotStartIndex:   0,
				SlotEndIndex:     1,
			},
		},
	}))

	ak := &mockAuthzGrantKeeper{}
	dec := NewBlsDealerAttemptDecorator(&bk, testMsgCodec(t), ak)
	return bk, blskeeper.NewMsgServerImpl(bk), ctx, dealer, ak, dec
}

// TestBlsDealerAttemptDecorator_PersistsAcrossHandlerRollback verifies BaseApp's
// branch semantics: when SubmitDealerPart rejects a polynomial with a slot root
// and rolls back the message branch, the ante branch's attempt increment still
// commits and blocks replays beyond max_dealer_attempts_per_epoch.
func TestBlsDealerAttemptDecorator_PersistsAcrossHandlerRollback(t *testing.T) {
	bk, ms, rootCtx, dealer, _, dec := setupDealerAttemptAnte(t)
	trapMsg := buildTrapDealerMsg(t, rootCtx.ChainID(), 1, dealer.String())

	runOneFinalizeTx := func() (handlerRan bool, err error) {
		anteMS := rootCtx.MultiStore().CacheMultiStore()
		anteCtx := rootCtx.WithMultiStore(anteMS)

		_, err = dec.AnteHandle(anteCtx, testFeeTx{msgs: []sdk.Msg{trapMsg}, gas: 1_000_000}, false,
			func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
				return ctx, nil
			})
		if err != nil {
			return false, err
		}
		// BaseApp commits ante branch before running message handlers.
		anteMS.Write()

		msgMS := rootCtx.MultiStore().CacheMultiStore()
		msgCtx := rootCtx.WithMultiStore(msgMS)
		handlerRan = true
		_, err = ms.SubmitDealerPart(msgCtx, trapMsg)
		if err == nil {
			msgMS.Write()
		}
		return handlerRan, err
	}

	for attempt := uint32(1); attempt <= blstypes.DefaultMaxDealerAttemptsPerEpoch; attempt++ {
		ran, err := runOneFinalizeTx()
		require.True(t, ran)
		require.ErrorContains(t, err, "evaluates to identity at slot 1")

		recorded, getErr := bk.GetDealerPartAttempts(rootCtx, 1, 0)
		require.NoError(t, getErr)
		require.Equal(t, attempt, recorded)
	}

	// Attempt #4 is rejected in ante before SubmitDealerPart runs any crypto.
	ran, err := runOneFinalizeTx()
	require.False(t, ran, "handler must not execute once attempt ceiling is reached")
	require.ErrorContains(t, err, "max dealer part attempts")
}

func TestBlsDealerAttemptDecorator_BundledMsgsRejectedBeforeHandler(t *testing.T) {
	bk, _, rootCtx, dealer, _, dec := setupDealerAttemptAnte(t)
	trapMsg := buildTrapDealerMsg(t, rootCtx.ChainID(), 1, dealer.String())

	// Bundling 4 dealer msgs in one tx trips the cap on msg #4 in ante; aborting
	// ante discards the ante cache so 0 handler executions happen.
	anteMS := rootCtx.MultiStore().CacheMultiStore()
	anteCtx := rootCtx.WithMultiStore(anteMS)
	nextCalled := false
	_, err := dec.AnteHandle(
		anteCtx,
		testFeeTx{msgs: []sdk.Msg{trapMsg, trapMsg, trapMsg, trapMsg}, gas: 2_000_000},
		false,
		func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
			nextCalled = true
			return ctx, nil
		},
	)
	require.ErrorContains(t, err, "max dealer part attempts")
	require.False(t, nextCalled)

	recorded, getErr := bk.GetDealerPartAttempts(rootCtx, 1, 0)
	require.NoError(t, getErr)
	require.Equal(t, uint32(0), recorded)
}

func TestBlsDealerAttemptDecorator_UnauthorizedMsgExecCannotBurnParticipantQuota(t *testing.T) {
	bk, _, rootCtx, dealer, ak, dec := setupDealerAttemptAnte(t)
	attacker := sdk.AccAddress("attacker_account_001")
	warmKey := sdk.AccAddress("warm_key_account_001")
	trapMsg := buildTrapDealerMsg(t, rootCtx.ChainID(), 1, dealer.String())

	// Even in FinalizeBlock on a paid MsgExec (where MsgExecAuthorizationDecorator
	// does not run), an unauthorized grantee cannot burn dealer's attempt quota.
	unauthExec := wrapExec(t, attacker.String(), trapMsg)
	nextCalled := false
	_, err := dec.AnteHandle(rootCtx, testFeeTx{msgs: []sdk.Msg{unauthExec}, gas: 500_000}, false,
		func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
			nextCalled = true
			return ctx, nil
		})
	require.ErrorIs(t, err, authz.ErrNoAuthorizationFound)
	require.False(t, nextCalled)

	recorded, getErr := bk.GetDealerPartAttempts(rootCtx, 1, 0)
	require.NoError(t, getErr)
	require.Equal(t, uint32(0), recorded)

	// Nested MsgExec carrying MsgSubmitDealerPart is rejected even if paid.
	ak.save(warmKey, dealer, authz.NewGenericAuthorization(sdk.MsgTypeURL(trapMsg)))
	nestedExec := wrapExec(t, warmKey.String(), wrapExec(t, warmKey.String(), trapMsg))
	_, err = dec.AnteHandle(rootCtx, testFeeTx{msgs: []sdk.Msg{nestedExec}, gas: 500_000}, false,
		func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
			return ctx, nil
		})
	require.ErrorIs(t, err, errNestedMsgExec)

	// Unrelated MsgExec (e.g. bank MsgSend) passes untouched without grant lookup here.
	sendAny, err := codectypes.NewAnyWithValue(&banktypes.MsgSend{
		FromAddress: dealer.String(),
		ToAddress:   attacker.String(),
	})
	require.NoError(t, err)
	unrelatedExec := &authz.MsgExec{Grantee: attacker.String(), Msgs: []*codectypes.Any{sendAny}}
	passed := false
	_, err = dec.AnteHandle(rootCtx, testFeeTx{msgs: []sdk.Msg{unrelatedExec}, gas: 200_000}, false,
		func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
			passed = true
			return ctx, nil
		})
	require.NoError(t, err)
	require.True(t, passed)

	// Authorized warm key is counted up to max_dealer_attempts_per_epoch and then capped.
	authExec := wrapExec(t, warmKey.String(), trapMsg)
	for i := uint32(1); i <= blstypes.DefaultMaxDealerAttemptsPerEpoch; i++ {
		_, err = dec.AnteHandle(rootCtx, testFeeTx{msgs: []sdk.Msg{authExec}, gas: 500_000}, false,
			func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
				return ctx, nil
			})
		require.NoError(t, err)
	}
	_, err = dec.AnteHandle(rootCtx, testFeeTx{msgs: []sdk.Msg{authExec}, gas: 500_000}, false,
		func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
			return ctx, nil
		})
	require.ErrorContains(t, err, "max dealer part attempts")
}
