package app_test

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"cosmossdk.io/math"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

type bridgeCheckTxFixture struct {
	testApp   *app.App
	validator sdk.AccAddress
	valPriv   cryptotypes.PrivKey
	warmKey   sdk.AccAddress
	warmPriv  cryptotypes.PrivKey
	btx       *inferencetypes.BridgeTransaction
	bridgeMsg *inferencetypes.MsgBridgeExchange
}

// setupDoomedBridgeCheckTx seeds the Kaitaku production shape: validator is
// active but not in the stamped SDK group for an existing bridge transaction.
func setupDoomedBridgeCheckTx(t *testing.T) bridgeCheckTxFixture {
	t.Helper()
	testApp := createTestApp(t)

	// Seed into the root multistore. Writes on finalizeBlockState after FinalizeBlock
	// are lost on Commit (workingHash already Write()'d), so use NewUncachedContext.
	ctx := testApp.NewUncachedContext(false, cmtproto.Header{
		Height:  testApp.LastBlockHeight(),
		ChainID: TallyTestChainID,
		Time:    time.Now().UTC(),
	})

	valPriv := secp256k1.GenPrivKey()
	validator := sdk.AccAddress(valPriv.PubKey().Address())
	otherPriv := secp256k1.GenPrivKey()
	otherAddr := sdk.AccAddress(otherPriv.PubKey().Address())
	warmPriv := secp256k1.GenPrivKey()
	warmKey := sdk.AccAddress(warmPriv.PubKey().Address())

	fundAccount(t, ctx, testApp, validator, valPriv.PubKey())
	fundAccount(t, ctx, testApp, otherAddr, otherPriv.PubKey())
	fundAccount(t, ctx, testApp, warmKey, warmPriv.PubKey())

	const epochIndex = uint64(335)
	require.NoError(t, testApp.InferenceKeeper.SetEffectiveEpochIndex(ctx, epochIndex))
	require.NoError(t, testApp.InferenceKeeper.SetEpoch(ctx, &inferencetypes.Epoch{
		Index:               epochIndex,
		PocStartBlockHeight: 1,
	}))
	require.NoError(t, testApp.InferenceKeeper.SetActiveParticipants(ctx, inferencetypes.ActiveParticipants{
		EpochId:      epochIndex,
		Participants: []*inferencetypes.ActiveParticipant{{Index: validator.String()}},
	}))

	// Group members: only `other` — validator is active but not in stamped SDK group
	// (Kaitaku production shape after mid-epoch RemoveMember).
	groupID := createEpochGroupWithMember(t, ctx, testApp, otherAddr)
	testApp.InferenceKeeper.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex:   epochIndex,
		ModelId:      "",
		EpochGroupId: groupID,
		TotalWeight:  100,
	})

	btx := &inferencetypes.BridgeTransaction{
		ChainId:         "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "32914000000000",
		BlockNumber:     "25586367",
		ReceiptIndex:    "144",
		ReceiptsRoot:    "0xroot",
		EpochIndex:      epochIndex,
		Status:          inferencetypes.BridgeTransactionStatus_BRIDGE_COMPLETED,
	}
	testApp.InferenceKeeper.SetBridgeTransaction(ctx, btx)

	_, err := testApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: testApp.LastBlockHeight() + 1,
		Time:   time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = testApp.Commit()
	require.NoError(t, err)

	checkCtx := testApp.NewContext(true)
	before, found := testApp.InferenceKeeper.GetBridgeTransactionByContent(checkCtx, btx)
	require.True(t, found)
	require.Empty(t, before.Validators)

	bridgeMsg := &inferencetypes.MsgBridgeExchange{
		Validator:       validator.String(),
		OriginChain:     btx.ChainId,
		ContractAddress: btx.ContractAddress,
		OwnerAddress:    btx.OwnerAddress,
		OwnerPubKey:     "034daac7b5be60532b562b030824b2c9b7c450985b894dd5544f9c7dd807d8bb88",
		Amount:          btx.Amount,
		BlockNumber:     btx.BlockNumber,
		ReceiptIndex:    btx.ReceiptIndex,
		ReceiptsRoot:    btx.ReceiptsRoot,
	}

	return bridgeCheckTxFixture{
		testApp:   testApp,
		validator: validator,
		valPriv:   valPriv,
		warmKey:   warmKey,
		warmPriv:  warmPriv,
		btx:       btx,
		bridgeMsg: bridgeMsg,
	}
}

func assertBridgeCheckTxRejects(t *testing.T, f bridgeCheckTxFixture, msgs []sdk.Msg, signer sdk.AccAddress, signerPriv cryptotypes.PrivKey) {
	t.Helper()
	checkCtx := f.testApp.NewContext(true)
	acc := f.testApp.AccountKeeper.GetAccount(checkCtx, signer)
	require.NotNil(t, acc)

	tx, err := simtestutil.GenSignedMockTx(
		rand.New(rand.NewSource(1)),
		f.testApp.TxConfig(),
		msgs,
		sdk.NewCoins(sdk.NewInt64Coin(inferencetypes.BaseCoin, 1_000_000)),
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
	require.NotEqual(t, uint32(0), resp.Code, "CheckTx must reject doomed bridge vote (got code=0 log=%q)", resp.Log)
	require.True(t,
		strings.Contains(strings.ToLower(resp.Log), strings.ToLower(inferencetypes.ErrBridgeValidatorNotInTxEpochGroup.Error())),
		"CheckTx log must contain registered ErrBridgeValidatorNotInTxEpochGroup message; got code=%d log=%q", resp.Code, resp.Log,
	)

	after, found := f.testApp.InferenceKeeper.GetBridgeTransactionByContent(
		f.testApp.NewContext(true), f.btx)
	require.True(t, found)
	require.Empty(t, after.Validators, "message handler must not run during CheckTx")
}

// TestBridgeExchangeEarlyReject_CheckTxViaBaseApp proves the wired ante chain
// (not the decorator alone) rejects a doomed MsgBridgeExchange during CheckTx
// with Code != 0 and the RawLog substring the DAPI #1478 classifier matches.
//
// This closes the #1478 gap: unit tests that only call AnteHandle + synthesize
// "bridge exchange failed: code=5 rawLog=..." never proved BaseApp CheckTx
// surfaces ante errors. SDK 0.53 also does not run message handlers in CheckTx;
// we assert the bridge validators set is unchanged as evidence the handler did
// not execute.
func TestBridgeExchangeEarlyReject_CheckTxViaBaseApp(t *testing.T) {
	f := setupDoomedBridgeCheckTx(t)
	assertBridgeCheckTxRejects(t, f, []sdk.Msg{f.bridgeMsg}, f.validator, f.valPriv)
}

// TestBridgeExchangeEarlyReject_CheckTxViaBaseApp_MsgExec is the production
// warm-key path: DAPI wraps MsgBridgeExchange in authz.MsgExec when the
// feeder signer is not the main account (tx_manager broadcastMessagesAtAttempt).
// Without ante unwrap, CheckTx would only see MsgExec and admit a doomed vote.
func TestBridgeExchangeEarlyReject_CheckTxViaBaseApp_MsgExec(t *testing.T) {
	f := setupDoomedBridgeCheckTx(t)
	execMsg := authztypes.NewMsgExec(f.warmKey, []sdk.Msg{f.bridgeMsg})
	assertBridgeCheckTxRejects(t, f, []sdk.Msg{&execMsg}, f.warmKey, f.warmPriv)
}

func fundAccount(t *testing.T, ctx sdk.Context, testApp *app.App, addr sdk.AccAddress, pub cryptotypes.PubKey) {
	t.Helper()
	acc := testApp.AccountKeeper.NewAccountWithAddress(ctx, addr)
	if err := acc.SetPubKey(pub); err != nil {
		require.NoError(t, err)
	}
	testApp.AccountKeeper.SetAccount(ctx, acc)

	coins := sdk.NewCoins(sdk.NewCoin(inferencetypes.BaseCoin, math.NewInt(100_000_000_000)))
	require.NoError(t, testApp.BankKeeper.MintCoins(ctx, inferencetypes.TopRewardPoolAccName, coins))
	require.NoError(t, testApp.BankKeeper.SendCoinsFromModuleToAccount(ctx, inferencetypes.TopRewardPoolAccName, addr, coins))
}

func createEpochGroupWithMember(t *testing.T, ctx sdk.Context, testApp *app.App, member sdk.AccAddress) uint64 {
	t.Helper()
	admin := testApp.InferenceKeeper.GetAuthority()
	msg := &group.MsgCreateGroupWithPolicy{
		Admin: admin,
		Members: []group.MemberRequest{{
			Address:  member.String(),
			Weight:   "10",
			Metadata: "",
		}},
		GroupMetadata: "",
	}
	policy := group.NewPercentageDecisionPolicy("0.50", 4*time.Minute, 0)
	require.NoError(t, msg.SetDecisionPolicy(policy))
	resp, err := testApp.GroupKeeper.CreateGroupWithPolicy(ctx, msg)
	require.NoError(t, err)
	require.NotZero(t, resp.GroupId)
	return resp.GroupId
}
