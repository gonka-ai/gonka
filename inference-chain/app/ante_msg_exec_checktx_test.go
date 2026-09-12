package app_test

import (
	"math/rand"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authz "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

const seedSig64 = "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

type msgExecCheckTxFixture struct {
	testApp    *app.App
	granter    sdk.AccAddress
	granterKey cryptotypes.PrivKey
	grantee    sdk.AccAddress
	granteeKey cryptotypes.PrivKey
	seedMsg    *inferencetypes.MsgSubmitSeed
	pocMsg     *inferencetypes.MsgSubmitPocValidationsV2
}

func setupMsgExecCheckTx(t *testing.T, registerParticipant bool) msgExecCheckTxFixture {
	t.Helper()
	testApp := createTestApp(t)

	ctx := testApp.NewUncachedContext(false, cmtproto.Header{
		Height:  testApp.LastBlockHeight(),
		ChainID: TallyTestChainID,
		Time:    time.Now().UTC(),
	})

	granterKey := secp256k1.GenPrivKey()
	granter := sdk.AccAddress(granterKey.PubKey().Address())
	granteeKey := secp256k1.GenPrivKey()
	grantee := sdk.AccAddress(granteeKey.PubKey().Address())

	fundAccount(t, ctx, testApp, granter, granterKey.PubKey())
	fundAccount(t, ctx, testApp, grantee, granteeKey.PubKey())

	const epochIndex = uint64(1)
	require.NoError(t, testApp.InferenceKeeper.SetEffectiveEpochIndex(ctx, 0))
	require.NoError(t, testApp.InferenceKeeper.SetEpoch(ctx, &inferencetypes.Epoch{
		Index:               0,
		PocStartBlockHeight: 0,
	}))
	require.NoError(t, testApp.InferenceKeeper.SetEpoch(ctx, &inferencetypes.Epoch{
		Index:               epochIndex,
		PocStartBlockHeight: 1,
	}))

	params, err := testApp.InferenceKeeper.GetParams(ctx)
	require.NoError(t, err)
	if params.PocParams == nil {
		params.PocParams = inferencetypes.DefaultPocParams()
	}
	params.PocParams.PocV2Enabled = true
	require.NoError(t, testApp.InferenceKeeper.SetParams(ctx, params))

	if registerParticipant {
		require.NoError(t, testApp.InferenceKeeper.Participants.Set(ctx, granter, inferencetypes.Participant{
			Index:   granter.String(),
			Address: granter.String(),
		}))
	}

	_, err = testApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: testApp.LastBlockHeight() + 1,
		Time:   time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = testApp.Commit()
	require.NoError(t, err)

	return msgExecCheckTxFixture{
		testApp:    testApp,
		granter:    granter,
		granterKey: granterKey,
		grantee:    grantee,
		granteeKey: granteeKey,
		seedMsg: &inferencetypes.MsgSubmitSeed{
			Creator:    granter.String(),
			EpochIndex: epochIndex,
			Signature:  seedSig64,
		},
		pocMsg: &inferencetypes.MsgSubmitPocValidationsV2{
			Creator:                  granter.String(),
			PocStageStartBlockHeight: 1,
			Validations: []*inferencetypes.PoCValidationEntryV2{
				{ParticipantAddress: grantee.String(), ModelId: "test-model", ValidatedWeight: 100},
			},
		},
	}
}

func persistGrant(t *testing.T, f msgExecCheckTxFixture, authorization authz.Authorization, expiration *time.Time) {
	t.Helper()
	grantCtx := f.testApp.NewUncachedContext(false, cmtproto.Header{
		Height:  f.testApp.LastBlockHeight(),
		ChainID: TallyTestChainID,
		Time:    time.Now().UTC(),
	})
	require.NoError(t, f.testApp.AuthzKeeper.SaveGrant(grantCtx, f.grantee, f.granter, authorization, expiration))
	_, err := f.testApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: f.testApp.LastBlockHeight() + 1,
		Time:   time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = f.testApp.Commit()
	require.NoError(t, err)
}

func signCheckTx(t *testing.T, f msgExecCheckTxFixture, msgs []sdk.Msg, signer sdk.AccAddress, signerPriv cryptotypes.PrivKey) ([]byte, *abci.ResponseCheckTx) {
	t.Helper()
	checkCtx := f.testApp.NewContext(true)
	acc := f.testApp.AccountKeeper.GetAccount(checkCtx, signer)
	require.NotNil(t, acc)

	tx, err := simtestutil.GenSignedMockTx(
		rand.New(rand.NewSource(1)),
		f.testApp.TxConfig(),
		msgs,
		sdk.NewCoins(),
		simtestutil.DefaultGenTxGas,
		TallyTestChainID,
		[]uint64{acc.GetAccountNumber()},
		[]uint64{acc.GetSequence()},
		signerPriv,
	)
	require.NoError(t, err)
	txBytes, err := f.testApp.TxConfig().TxEncoder()(tx)
	require.NoError(t, err)

	resp, err := f.testApp.CheckTx(&abci.RequestCheckTx{Tx: txBytes})
	require.NoError(t, err)
	return txBytes, resp
}

func TestMsgExecAuthorization_DirectRegisteredParticipantPassesCheckTx(t *testing.T) {
	f := setupMsgExecCheckTx(t, true)
	_, resp := signCheckTx(t, f, []sdk.Msg{f.pocMsg}, f.granter, f.granterKey)
	require.Equal(t, uint32(0), resp.Code, "CheckTx log=%q", resp.Log)
}

func TestMsgExecAuthorization_DirectNonparticipantFailsCheckTx(t *testing.T) {
	f := setupMsgExecCheckTx(t, false)
	_, resp := signCheckTx(t, f, []sdk.Msg{f.pocMsg}, f.granter, f.granterKey)
	require.NotEqual(t, uint32(0), resp.Code, "nonparticipant PoC message must fail CheckTx; log=%q", resp.Log)
	require.Equal(t, 0, f.testApp.Mempool().CountTx(), "rejected tx must not enter the mempool")
}

func TestMsgExecAuthorization_AuthorizedWrapperCheckTxAndFinalizeBlock(t *testing.T) {
	f := setupMsgExecCheckTx(t, true)
	persistGrant(t, f, authz.NewGenericAuthorization(sdk.MsgTypeURL(f.seedMsg)), nil)

	execMsg := authz.NewMsgExec(f.grantee, []sdk.Msg{f.seedMsg})
	txBytes, resp := signCheckTx(t, f, []sdk.Msg{&execMsg}, f.grantee, f.granteeKey)
	require.Equal(t, uint32(0), resp.Code, "authorized wrapper CheckTx log=%q", resp.Log)

	finalizeResp, err := f.testApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: f.testApp.LastBlockHeight() + 1,
		Time:   time.Now().UTC(),
		Txs:    [][]byte{txBytes},
	})
	require.NoError(t, err)
	require.NotEmpty(t, finalizeResp.TxResults)
	require.Equal(t, uint32(0), finalizeResp.TxResults[0].Code, "FinalizeBlock log=%q", finalizeResp.TxResults[0].Log)

	_, err = f.testApp.Commit()
	require.NoError(t, err)

	stored, found := f.testApp.InferenceKeeper.GetRandomSeed(f.testApp.NewContext(true), f.seedMsg.EpochIndex, f.granter.String())
	require.True(t, found, "authorized wrapper must execute SubmitSeed")
	require.Equal(t, f.seedMsg.Signature, stored.Signature)
}

func TestMsgExecAuthorization_UnauthorizedWrapperRejectedFromMempool(t *testing.T) {
	f := setupMsgExecCheckTx(t, true)

	execMsg := authz.NewMsgExec(f.grantee, []sdk.Msg{f.seedMsg})
	_, resp := signCheckTx(t, f, []sdk.Msg{&execMsg}, f.grantee, f.granteeKey)
	require.NotEqual(t, uint32(0), resp.Code, "unauthorized wrapper must fail CheckTx; log=%q", resp.Log)
	require.Equal(t, 0, f.testApp.Mempool().CountTx(), "unauthorized wrapper must never reach the mempool")

	_, found := f.testApp.InferenceKeeper.GetRandomSeed(f.testApp.NewContext(true), f.seedMsg.EpochIndex, f.granter.String())
	require.False(t, found, "message handler must not run for a CheckTx rejection")
}

func TestMsgExecAuthorization_ExpiredGrantFailsCheckTx(t *testing.T) {
	f := setupMsgExecCheckTx(t, true)

	now := time.Now().UTC()
	expires := now.Add(time.Minute)
	grantCtx := f.testApp.NewUncachedContext(false, cmtproto.Header{
		Height:  f.testApp.LastBlockHeight(),
		ChainID: TallyTestChainID,
		Time:    now,
	})
	require.NoError(t, f.testApp.AuthzKeeper.SaveGrant(
		grantCtx,
		f.grantee,
		f.granter,
		authz.NewGenericAuthorization(sdk.MsgTypeURL(f.seedMsg)),
		&expires,
	))
	_, err := f.testApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: f.testApp.LastBlockHeight() + 1,
		Time:   now,
	})
	require.NoError(t, err)
	_, err = f.testApp.Commit()
	require.NoError(t, err)

	_, err = f.testApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: f.testApp.LastBlockHeight() + 1,
		Time:   expires.Add(time.Hour),
	})
	require.NoError(t, err)
	_, err = f.testApp.Commit()
	require.NoError(t, err)

	execMsg := authz.NewMsgExec(f.grantee, []sdk.Msg{f.seedMsg})
	_, resp := signCheckTx(t, f, []sdk.Msg{&execMsg}, f.grantee, f.granteeKey)
	require.NotEqual(t, uint32(0), resp.Code, "expired grant must fail CheckTx; log=%q", resp.Log)
	require.Equal(t, 0, f.testApp.Mempool().CountTx())
}

func TestMsgExecAuthorization_WrongMessageTypeGrantFailsCheckTx(t *testing.T) {
	f := setupMsgExecCheckTx(t, true)
	persistGrant(t, f, authz.NewGenericAuthorization(sdk.MsgTypeURL(&inferencetypes.MsgSubmitHardwareDiff{})), nil)

	execMsg := authz.NewMsgExec(f.grantee, []sdk.Msg{f.seedMsg})
	_, resp := signCheckTx(t, f, []sdk.Msg{&execMsg}, f.grantee, f.granteeKey)
	require.NotEqual(t, uint32(0), resp.Code, "wrong-type grant must fail CheckTx; log=%q", resp.Log)
	require.Equal(t, 0, f.testApp.Mempool().CountTx())
}

func TestMsgExecAuthorization_GranterEqualsGranteePassesWithoutGrant(t *testing.T) {
	f := setupMsgExecCheckTx(t, true)

	execMsg := authz.NewMsgExec(f.granter, []sdk.Msg{f.seedMsg})
	_, resp := signCheckTx(t, f, []sdk.Msg{&execMsg}, f.granter, f.granterKey)
	require.Equal(t, uint32(0), resp.Code, "self-exec CheckTx log=%q", resp.Log)
}

func TestMsgExecAuthorization_NonparticipantGranterFailsEvenWithGrant(t *testing.T) {
	f := setupMsgExecCheckTx(t, false)
	persistGrant(t, f, authz.NewGenericAuthorization(sdk.MsgTypeURL(f.pocMsg)), nil)

	execMsg := authz.NewMsgExec(f.grantee, []sdk.Msg{f.pocMsg})
	_, resp := signCheckTx(t, f, []sdk.Msg{&execMsg}, f.grantee, f.granteeKey)
	require.NotEqual(t, uint32(0), resp.Code, "nonparticipant granter must fail CheckTx even with a grant; log=%q", resp.Log)
	require.Equal(t, 0, f.testApp.Mempool().CountTx())
}

func TestMsgExecAuthorization_NestedMsgExecFailsClosed(t *testing.T) {
	f := setupMsgExecCheckTx(t, true)
	persistGrant(t, f, authz.NewGenericAuthorization(sdk.MsgTypeURL(f.seedMsg)), nil)

	innerExec := authz.NewMsgExec(f.grantee, []sdk.Msg{f.seedMsg})
	outer := authz.NewMsgExec(f.grantee, []sdk.Msg{&innerExec})
	_, resp := signCheckTx(t, f, []sdk.Msg{&outer}, f.grantee, f.granteeKey)
	require.NotEqual(t, uint32(0), resp.Code, "nested MsgExec must fail closed; log=%q", resp.Log)
	require.Equal(t, 0, f.testApp.Mempool().CountTx())
}

func TestMsgExecAuthorization_MalformedMsgExecFailsClosed(t *testing.T) {
	f := setupMsgExecCheckTx(t, true)

	execMsg := &authz.MsgExec{
		Grantee: f.grantee.String(),
		Msgs: []*codectypes.Any{{
			TypeUrl: sdk.MsgTypeURL(f.seedMsg),
			Value:   []byte{0xff, 0x00, 0x01},
		}},
	}
	_, resp := signCheckTx(t, f, []sdk.Msg{execMsg}, f.grantee, f.granteeKey)
	require.NotEqual(t, uint32(0), resp.Code, "malformed MsgExec must fail closed; log=%q", resp.Log)
	require.Equal(t, 0, f.testApp.Mempool().CountTx())
}
