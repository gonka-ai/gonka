package app_test

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"math/rand"
	"testing"
	"time"

	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/math"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fp"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/hash_to_curve"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/app"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencemodule "github.com/productscience/inference/x/inference/module"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/sha3"
)

const reissueTestBridge = "0x1111111111111111111111111111111111111111"
const reissueTestEpoch = uint64(700)

type bridgeReissueAppFixture struct {
	app       *app.App
	ctx       sdk.Context
	owner     sdk.AccAddress
	ownerKey  cryptotypes.PrivKey
	outsider  sdk.AccAddress
	requestID string
	blsID     []byte
	first     *blstypes.ThresholdSigningRequest
	mint      inferencetypes.MsgRequestBridgeMint
}

// This fixture sends the real mint message through the application's registered
// Msg service, locks real bank coins and completes real BLS aggregation. It does
// not fabricate an already-completed request or install mock refund hooks.
func newBridgeReissueAppFixture(t *testing.T, eligible bool) *bridgeReissueAppFixture {
	t.Helper()
	testApp := createTestApp(t)
	ctx := testApp.NewUncachedContext(false, cmtproto.Header{
		Height: testApp.LastBlockHeight(), ChainID: TallyTestChainID,
	}).WithTxBytes([]byte("bridge-reissue-initial-mint"))
	f := &bridgeReissueAppFixture{app: testApp, ctx: ctx}
	for _, address := range []*sdk.AccAddress{&f.owner, &f.outsider} {
		privateKey := secp256k1.GenPrivKey()
		if address == &f.owner {
			f.ownerKey = privateKey
		}
		*address = sdk.AccAddress(privateKey.PubKey().Address())
		account := testApp.AccountKeeper.NewAccountWithAddress(ctx, *address)
		require.NoError(t, account.SetPubKey(privateKey.PubKey()))
		testApp.AccountKeeper.SetAccount(ctx, account)
	}
	coins := sdk.NewCoins(sdk.NewInt64Coin(inferencetypes.BaseCoin, 1_000))
	require.NoError(t, testApp.BankKeeper.MintCoins(ctx, inferencetypes.TopRewardPoolAccName, coins))
	require.NoError(t, testApp.BankKeeper.SendCoinsFromModuleToAccount(ctx, inferencetypes.TopRewardPoolAccName, f.owner, coins))
	testApp.InferenceKeeper.SetBridgeContractAddress(ctx, inferencetypes.BridgeContractAddress{
		ChainId: "ethereum", Address: reissueTestBridge,
	})
	f.setAllowed(t, eligible)
	f.setEpoch(t, reissueTestEpoch)
	msg := &inferencetypes.MsgRequestBridgeMint{
		Creator: f.owner.String(), Amount: "100", ChainId: "ethereum",
		DestinationAddress:       "0x2222222222222222222222222222222222222222",
		DestinationBridgeAddress: reissueTestBridge,
	}
	f.mint = *msg
	handler := testApp.MsgServiceRouter().Handler(msg)
	require.NotNil(t, handler)
	result, err := handler(ctx, msg)
	require.NoError(t, err)
	require.Len(t, result.MsgResponses, 1)
	var response inferencetypes.MsgRequestBridgeMintResponse
	require.NoError(t, testApp.AppCodec().Unmarshal(result.MsgResponses[0].Value, &response))
	f.requestID = response.RequestId
	hash := sha3.NewLegacyKeccak256()
	hash.Write([]byte(f.requestID))
	f.blsID = hash.Sum(nil)
	pending, err := testApp.InferenceKeeper.BridgeMintRefundsMap.Has(ctx, hex.EncodeToString(f.blsID))
	require.NoError(t, err)
	require.True(t, pending)
	f.assertFunds(t)
	f.complete(t)
	f.first = f.status(t)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED, f.first.Status)
	if eligible {
		require.Equal(t, []string{f.owner.String()}, f.first.ReissueSigners)
	} else {
		require.Empty(t, f.first.ReissueSigners)
	}
	pending, err = testApp.InferenceKeeper.BridgeMintRefundsMap.Has(ctx, hex.EncodeToString(f.blsID))
	require.NoError(t, err)
	require.False(t, pending, "the installed completion hook must remove pending refund context")
	return f
}

func (f *bridgeReissueAppFixture) setAllowed(t *testing.T, allowed bool) {
	t.Helper()
	params, err := f.app.InferenceKeeper.GetParams(f.ctx)
	require.NoError(t, err)
	params.BridgeReissueParams = &inferencetypes.BridgeReissueParams{}
	if allowed {
		params.BridgeReissueParams.AllowedTargets = []inferencetypes.BridgeReissueTarget{{ChainId: "ethereum", ContractAddress: reissueTestBridge}}
	}
	require.NoError(t, f.app.InferenceKeeper.SetParams(f.ctx, params))
}

func (f *bridgeReissueAppFixture) setEpoch(t *testing.T, epoch uint64) {
	t.Helper()
	f.app.InferenceKeeper.SetEpochGroupData(f.ctx, inferencetypes.EpochGroupData{EpochIndex: epoch})
	require.NoError(t, f.app.InferenceKeeper.SetEffectiveEpochIndex(f.ctx, epoch))
	// Each epoch has a different constant one-slot secret polynomial.
	secret := big.NewInt(int64(epoch + 1))
	_, _, _, generator := bls12381.Generators()
	var publicKey bls12381.G2Affine
	publicKey.ScalarMultiplication(&generator, secret)
	encoded := publicKey.Bytes()
	require.NoError(t, f.app.BlsKeeper.SetEpochBLSData(f.ctx, blstypes.EpochBLSData{
		EpochId: epoch, DkgPhase: blstypes.DKGPhase_DKG_PHASE_SIGNED,
		ITotalSlots: 1, TSlotsDegree: 0, GroupPublicKey: encoded[:], SlotPublicKeys: [][]byte{encoded[:]},
		Participants: []blstypes.BLSParticipantInfo{{Address: f.owner.String(), PercentageWeight: math.LegacyOneDec(), SlotStartIndex: 0, SlotEndIndex: 0}},
	}))
}

func (f *bridgeReissueAppFixture) status(t *testing.T) *blstypes.ThresholdSigningRequest {
	t.Helper()
	request, err := f.app.BlsKeeper.GetSigningStatus(f.ctx, f.blsID)
	require.NoError(t, err)
	return request
}

func (f *bridgeReissueAppFixture) complete(t *testing.T) {
	t.Helper()
	request := f.status(t)
	var element fp.Element
	element.SetBytes(request.MessageHash)
	mapped := bls12381.MapToCurve1(&element)
	hash_to_curve.G1Isogeny(&mapped.X, &mapped.Y)
	var message, signature bls12381.G1Affine
	message.ClearCofactor(&mapped)
	signature.ScalarMultiplication(&message, big.NewInt(int64(request.CurrentEpochId+1)))
	encoded := signature.Bytes()
	require.NoError(t, f.app.BlsKeeper.AddPartialSignature(f.ctx, f.blsID, []uint32{0}, encoded[:], f.owner.String()))
}

func (f *bridgeReissueAppFixture) reissue(t *testing.T, creator sdk.AccAddress) error {
	t.Helper()
	msg := &inferencetypes.MsgReissueBridgeSignature{Creator: creator.String(), RequestId: f.requestID}
	// The registered codec and signer descriptor must round-trip this new RPC.
	builder := f.app.TxConfig().NewTxBuilder()
	require.NoError(t, builder.SetMsgs(msg))
	encoded, err := f.app.TxConfig().TxEncoder()(builder.GetTx())
	require.NoError(t, err)
	decoded, err := f.app.TxConfig().TxDecoder()(encoded)
	require.NoError(t, err)
	require.Len(t, decoded.GetMsgs(), 1)
	require.Equal(t, msg, decoded.GetMsgs()[0])
	signers, _, err := f.app.AppCodec().GetMsgV1Signers(msg)
	require.NoError(t, err)
	require.Equal(t, [][]byte{creator.Bytes()}, signers)
	handler := f.app.MsgServiceRouter().Handler(decoded.GetMsgs()[0])
	require.NotNil(t, handler, "new RPC must be registered by the real app")
	result, err := handler(f.ctx, decoded.GetMsgs()[0])
	if err == nil {
		require.Len(t, result.MsgResponses, 1)
		var response inferencetypes.MsgReissueBridgeSignatureResponse
		require.NoError(t, f.app.AppCodec().Unmarshal(result.MsgResponses[0].Value, &response))
		require.Equal(t, f.status(t).CurrentEpochId, response.EpochIndex)
	}
	return err
}

func (f *bridgeReissueAppFixture) assertFunds(t *testing.T) {
	t.Helper()
	require.Equal(t, sdk.NewInt64Coin(inferencetypes.BaseCoin, 900), f.app.BankKeeper.GetBalance(f.ctx, f.owner, inferencetypes.BaseCoin))
	require.Equal(t, sdk.NewInt64Coin(inferencetypes.BaseCoin, 100), f.app.InferenceKeeper.GetBridgeEscrowBalance(f.ctx, inferencetypes.BaseCoin))
}

func TestBridgeSignatureReissueThroughApp_CompletesWithoutMovingFunds(t *testing.T) {
	f := newBridgeReissueAppFixture(t, true)
	f.setEpoch(t, reissueTestEpoch+2)
	require.NoError(t, f.reissue(t, f.owner))
	collecting := f.status(t)
	require.Equal(t, f.first.FinalSignature, collecting.LastCompletedSignature)
	require.Equal(t, f.first.CurrentEpochId, collecting.LastCompletedEpochId)
	require.Equal(t, f.first.Data, collecting.Data)
	require.Equal(t, f.first.ChainId, collecting.ChainId)
	require.Equal(t, f.first.RequestId, collecting.RequestId)
	require.NotEqual(t, f.first.MessageHash, collecting.MessageHash)
	require.Empty(t, collecting.FinalSignature)
	f.assertFunds(t)
	require.Error(t, f.app.BlsKeeper.CancelThresholdSignature(f.ctx, f.blsID))
	refunded, err := f.app.InferenceKeeper.ProcessAutoRefundForFailedBridgeOperation(f.ctx, f.blsID, "test")
	require.ErrorContains(t, err, "has a completed signature; cannot refund")
	require.False(t, refunded)
	f.complete(t)
	completed := f.status(t)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED, completed.Status)
	require.Equal(t, reissueTestEpoch+2, completed.CurrentEpochId)
	require.NotEmpty(t, completed.FinalSignature)
	require.False(t, bytes.Equal(f.first.FinalSignature, completed.FinalSignature), "the new epoch signs a different message with a different key")
	f.assertFunds(t)
	require.Error(t, f.reissue(t, f.owner), "cannot start another reissue in the same epoch")
}

func TestBridgeSignatureReissueThroughApp_TimeoutRestoresProofWithoutRefund(t *testing.T) {
	f := newBridgeReissueAppFixture(t, true)
	// A failed completion cleanup can leave refund context behind even though
	// the signature completed. Reissue failure must clean it without refunding.
	require.NoError(t, f.app.InferenceKeeper.BridgeMintRefundsMap.Set(f.ctx, hex.EncodeToString(f.blsID), f.mint))
	f.setEpoch(t, reissueTestEpoch+2)
	require.NoError(t, f.reissue(t, f.owner))
	request := f.status(t)
	f.ctx = f.ctx.WithBlockHeight(request.DeadlineBlockHeight).WithEventManager(sdk.NewEventManager())
	module, ok := f.app.ModuleManager.Modules[blstypes.ModuleName].(appmodule.HasEndBlocker)
	require.True(t, ok)
	require.NoError(t, module.EndBlock(f.ctx))
	restored := f.status(t)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED, restored.Status)
	require.Equal(t, f.first.FinalSignature, restored.FinalSignature)
	require.Equal(t, f.first.CurrentEpochId, restored.CurrentEpochId)
	require.Equal(t, f.first.MessageHash, restored.MessageHash)
	require.Equal(t, reissueTestEpoch+2, restored.LastReissueEpochId)
	for _, event := range f.ctx.EventManager().Events() {
		require.NotEqual(t, "bridge_operation_auto_refunded", event.Type)
	}
	pending, err := f.app.InferenceKeeper.BridgeMintRefundsMap.Has(f.ctx, hex.EncodeToString(f.blsID))
	require.NoError(t, err)
	require.False(t, pending, "restoration must run the real idempotent completion cleanup")
	f.assertFunds(t)
	require.Error(t, f.reissue(t, f.owner), "a failed epoch attempt cannot be retried in a loop")
	f.setEpoch(t, reissueTestEpoch+3)
	require.NoError(t, f.reissue(t, f.owner))
	f.complete(t)
	f.assertFunds(t)
}

func TestBridgeSignatureReissueThroughApp_PolicyAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name         string
		prepare      func(*testing.T, *bridgeReissueAppFixture)
		unauthorized bool
	}{
		{name: "another account", unauthorized: true},
		{name: "allowlist removed", prepare: func(t *testing.T, f *bridgeReissueAppFixture) { f.setAllowed(t, false) }},
		{name: "registry removed", prepare: func(t *testing.T, f *bridgeReissueAppFixture) {
			f.app.InferenceKeeper.RemoveBridgeContractAddress(f.ctx, "ethereum", reissueTestBridge)
		}},
		{name: "too early", prepare: func(t *testing.T, f *bridgeReissueAppFixture) { f.setEpoch(t, reissueTestEpoch+1) }},
		{name: "wrong source chain", prepare: func(t *testing.T, f *bridgeReissueAppFixture) { f.ctx = f.ctx.WithChainID("foreign-chain") }},
		{name: "same contract on different destination chain", prepare: func(t *testing.T, f *bridgeReissueAppFixture) {
			params, err := f.app.InferenceKeeper.GetParams(f.ctx)
			require.NoError(t, err)
			params.BridgeReissueParams.AllowedTargets[0].ChainId = "polygon"
			require.NoError(t, f.app.InferenceKeeper.SetParams(f.ctx, params))
			f.app.InferenceKeeper.SetBridgeContractAddress(f.ctx, inferencetypes.BridgeContractAddress{ChainId: "polygon", Address: reissueTestBridge})
		}},
		{name: "different contract on same destination chain", prepare: func(t *testing.T, f *bridgeReissueAppFixture) {
			const otherBridge = "0x3333333333333333333333333333333333333333"
			params, err := f.app.InferenceKeeper.GetParams(f.ctx)
			require.NoError(t, err)
			params.BridgeReissueParams.AllowedTargets[0].ContractAddress = otherBridge
			require.NoError(t, f.app.InferenceKeeper.SetParams(f.ctx, params))
			f.app.InferenceKeeper.SetBridgeContractAddress(f.ctx, inferencetypes.BridgeContractAddress{ChainId: "ethereum", Address: otherBridge})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBridgeReissueAppFixture(t, true)
			f.setEpoch(t, reissueTestEpoch+2)
			if tc.prepare != nil {
				tc.prepare(t, f)
			}
			creator := f.owner
			if tc.unauthorized {
				creator = f.outsider
			}
			before := f.status(t)
			require.Error(t, f.reissue(t, creator))
			require.Equal(t, before, f.status(t), "rejected reissue must not change signing state")
			f.assertFunds(t)
		})
	}
}

func TestBridgeSignatureReissueThroughApp_LegacyRequestCannotOptInLater(t *testing.T) {
	f := newBridgeReissueAppFixture(t, false)
	f.setAllowed(t, true)
	f.setEpoch(t, reissueTestEpoch+2)
	require.ErrorContains(t, f.reissue(t, f.owner), "not eligible")
	require.Equal(t, f.first, f.status(t))
	f.assertFunds(t)
}

func TestBridgeSignatureReissueAutoCLI(t *testing.T) {
	options := (inferencemodule.AppModule{}).AutoCLIOptions()
	for _, command := range options.Tx.RpcCommandOptions {
		if command.RpcMethod == "ReissueBridgeSignature" {
			require.Equal(t, "reissue-bridge-signature [request-id]", command.Use)
			require.Len(t, command.PositionalArgs, 1)
			require.Equal(t, "request_id", command.PositionalArgs[0].ProtoField)
			return
		}
	}
	t.Fatal("reissue RPC must be exposed by AutoCLI")
}

func TestBridgeSignatureReissueSignedTransactionThroughBaseApp(t *testing.T) {
	f := newBridgeReissueAppFixture(t, true)
	f.setEpoch(t, reissueTestEpoch+2)
	require.NoError(t, f.app.InferenceKeeper.SetEpoch(f.ctx, &inferencetypes.Epoch{Index: reissueTestEpoch + 2, PocStartBlockHeight: 1}))
	params, err := f.app.InferenceKeeper.GetParams(f.ctx)
	require.NoError(t, err)
	params.FeeParams.EnabledFeeGroups = append(params.FeeParams.EnabledFeeGroups, inferencetypes.FeeGroupBridge)
	params.FeeParams.Groups = append(params.FeeParams.Groups, &inferencetypes.FeeGroup{Name: inferencetypes.FeeGroupBridge, MinGasPrice: 1})
	require.NoError(t, f.app.InferenceKeeper.SetParams(f.ctx, params))
	const feeAmount int64 = 1_000_000
	extra := sdk.NewCoins(sdk.NewInt64Coin(inferencetypes.BaseCoin, 2*feeAmount))
	require.NoError(t, f.app.BankKeeper.MintCoins(f.ctx, inferencetypes.TopRewardPoolAccName, extra))
	require.NoError(t, f.app.BankKeeper.SendCoinsFromModuleToAccount(f.ctx, inferencetypes.TopRewardPoolAccName, f.owner, extra))
	_, err = f.app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: f.app.LastBlockHeight() + 1, Time: time.Now().UTC()})
	require.NoError(t, err)
	_, err = f.app.Commit()
	require.NoError(t, err)
	account := f.app.AccountKeeper.GetAccount(f.app.NewContext(true), f.owner)
	require.NotNil(t, account)
	msg := &inferencetypes.MsgReissueBridgeSignature{Creator: f.owner.String(), RequestId: f.requestID}
	encode := func(fees sdk.Coins) []byte {
		tx, err := simtestutil.GenSignedMockTx(rand.New(rand.NewSource(1)), f.app.TxConfig(), []sdk.Msg{msg},
			fees, uint64(feeAmount), TallyTestChainID, []uint64{account.GetAccountNumber()}, []uint64{account.GetSequence()}, f.ownerKey)
		require.NoError(t, err)
		encoded, err := f.app.TxConfig().TxEncoder()(tx)
		require.NoError(t, err)
		return encoded
	}
	unpaid, err := f.app.CheckTx(&abci.RequestCheckTx{Tx: encode(sdk.NewCoins())})
	require.NoError(t, err)
	require.NotZero(t, unpaid.Code, "reissue must obey the enabled bridge fee group")
	require.Contains(t, unpaid.Log, "insufficient fee")
	encoded := encode(sdk.NewCoins(sdk.NewInt64Coin(inferencetypes.BaseCoin, feeAmount)))
	check, err := f.app.CheckTx(&abci.RequestCheckTx{Tx: encoded})
	require.NoError(t, err)
	require.Zero(t, check.Code, "CheckTx: %s", check.Log)
	result, err := f.app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: f.app.LastBlockHeight() + 1, Time: time.Now().UTC(), Txs: [][]byte{encoded}})
	require.NoError(t, err)
	require.Len(t, result.TxResults, 1)
	require.Zero(t, result.TxResults[0].Code, "FinalizeBlock: %s", result.TxResults[0].Log)
	_, err = f.app.Commit()
	require.NoError(t, err)
	f.ctx = f.app.NewUncachedContext(false, cmtproto.Header{Height: f.app.LastBlockHeight(), ChainID: TallyTestChainID})
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES, f.status(t).Status)
	f.complete(t)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED, f.status(t).Status)
	require.Equal(t, reissueTestEpoch+2, f.status(t).CurrentEpochId)
	require.Equal(t, sdk.NewInt64Coin(inferencetypes.BaseCoin, 900+feeAmount), f.app.BankKeeper.GetBalance(f.ctx, f.owner, inferencetypes.BaseCoin), "only the explicit tx fee is deducted")
	require.Equal(t, sdk.NewInt64Coin(inferencetypes.BaseCoin, 100), f.app.InferenceKeeper.GetBridgeEscrowBalance(f.ctx, inferencetypes.BaseCoin), "reissue must not lock a second amount")
}
