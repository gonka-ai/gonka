package app_test

import (
	"math/rand"
	"testing"
	"time"

	"cosmossdk.io/math"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authz "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// warmKeyGasHeadroom is the cushion a client applies to the simulated gas.
// The cosmos fee group charges 1ngonka per gas, so the fee equals the gas limit.
const warmKeyGasHeadroomNumerator = 12
const warmKeyGasHeadroomDenominator = 10

// warmKeyTestGas is the gas limit and the fee, in ngonka, for a warm-key
// authz transaction in tests. TestWarmKeyLogGasEstimateCoversFinalize measured
// a group EXEC_TRY grant as the heaviest: 157466 simulated, 162168 finalized.
// 1.2× that estimate is 188959. 250000 covers that and a grant sent together
// with one other message.
const warmKeyTestGas = 250_000

// Test flow:
//  1. Simulate a marker grant, a replacement grant, a revoke, and a MsgExec of a grant.
//  2. Sign each one with gas and fee at 1.2× the simulation, which is the min gas price.
//  3. Assert FinalizeBlock accepts it and uses no more gas than that limit.
//     The simulation cache is discarded, so the log is still empty after Simulate.
func TestWarmKeyLogGasEstimateCoversFinalize(t *testing.T) {
	testApp := createTestApp(t)
	granterKey := secp256k1.GenPrivKey()
	granter := sdk.AccAddress(granterKey.PubKey().Address())
	ctx := testApp.NewUncachedContext(false, cmtproto.Header{
		Height:  testApp.LastBlockHeight(),
		ChainID: TallyTestChainID,
		Time:    time.Now().UTC(),
	})
	fundAccount(t, ctx, testApp, granter, granterKey.PubKey())
	at := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	commitWarmKeyGasBlock(t, testApp, at)

	grant := func(grantee sdk.AccAddress) sdk.Msg {
		msg, err := authz.NewMsgGrant(granter, grantee, authz.NewGenericAuthorization(inferencetypes.WarmKeyGrantMarkerTypeURL), nil)
		require.NoError(t, err)
		return msg
	}

	t.Run("grant", func(t *testing.T) {
		grantee := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
		measureWarmKeyGas(t, testApp, granter, granterKey, at, grant(grantee))
	})

	t.Run("replace", func(t *testing.T) {
		grantee := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
		deliverWarmKeyGasTx(t, testApp, granter, granterKey, at, simtestutil.DefaultGenTxGas, grant(grantee))
		measureWarmKeyGas(t, testApp, granter, granterKey, at, grant(grantee))
	})

	t.Run("revoke", func(t *testing.T) {
		grantee := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
		deliverWarmKeyGasTx(t, testApp, granter, granterKey, at, simtestutil.DefaultGenTxGas, grant(grantee))
		revoke := authz.NewMsgRevoke(granter, grantee, inferencetypes.WarmKeyGrantMarkerTypeURL)
		measureWarmKeyGas(t, testApp, granter, granterKey, at, &revoke)
	})

	t.Run("msg exec", func(t *testing.T) {
		grantee := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
		exec := authz.NewMsgExec(granter, []sdk.Msg{grant(grantee)})
		measureWarmKeyGas(t, testApp, granter, granterKey, at, &exec)
	})

	t.Run("group exec try", func(t *testing.T) {
		adminKey := secp256k1.GenPrivKey()
		admin := sdk.AccAddress(adminKey.PubKey().Address())
		fundAccount(t, warmKeyGasCtx(testApp), testApp, admin, adminKey.PubKey())
		policy := warmKeyGasGroupPolicy(t, testApp, admin)
		grantee := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
		inner, err := authz.NewMsgGrant(policy, grantee, authz.NewGenericAuthorization(inferencetypes.WarmKeyGrantMarkerTypeURL), nil)
		require.NoError(t, err)
		prop := &group.MsgSubmitProposal{
			GroupPolicyAddress: policy.String(),
			Proposers:          []string{admin.String()},
			Exec:               group.Exec_EXEC_TRY,
			Title:              "warm key",
			Summary:            "warm key",
		}
		require.NoError(t, prop.SetMsgs([]sdk.Msg{inner}))
		measureWarmKeyGas(t, testApp, admin, adminKey, at, prop)
	})
}

// measureWarmKeyGas simulates msg, then finalizes a copy whose gas limit and
// fee are 1.2× that estimate.
func measureWarmKeyGas(t *testing.T, testApp *app.App, signer sdk.AccAddress, priv cryptotypes.PrivKey, at time.Time, msg sdk.Msg) {
	t.Helper()
	probe := signWarmKeyTx(t, testApp, signer, priv, simtestutil.DefaultGenTxGas, msg)
	before, err := testApp.InferenceKeeper.WarmKeyLogEntries(warmKeyGasCtx(testApp), signer)
	require.NoError(t, err)
	sim, _, err := testApp.Simulate(probe)
	require.NoError(t, err)
	require.NotZero(t, sim.GasUsed)
	afterSim, err := testApp.InferenceKeeper.WarmKeyLogEntries(warmKeyGasCtx(testApp), signer)
	require.NoError(t, err)
	require.Len(t, afterSim, len(before), "simulation discards the log writes it metered")

	limit := sim.GasUsed * warmKeyGasHeadroomNumerator / warmKeyGasHeadroomDenominator
	got := deliverWarmKeyGasTx(t, testApp, signer, priv, at, limit, msg)
	t.Logf("simulated %d, limit %d (fee %dngonka), finalized %d", sim.GasUsed, limit, limit, got.GasUsed)
	require.LessOrEqual(t, got.GasUsed, int64(limit))
	require.NotZero(t, got.GasUsed)
}

func signWarmKeyTx(t *testing.T, testApp *app.App, signer sdk.AccAddress, priv cryptotypes.PrivKey, gas uint64, msgs ...sdk.Msg) []byte {
	t.Helper()
	acc := testApp.AccountKeeper.GetAccount(testApp.NewContext(true), signer)
	require.NotNil(t, acc)
	tx, err := simtestutil.GenSignedMockTx(
		rand.New(rand.NewSource(1)),
		testApp.TxConfig(),
		msgs,
		sdk.NewCoins(sdk.NewCoin(inferencetypes.BaseCoin, math.NewIntFromUint64(gas))),
		gas,
		TallyTestChainID,
		[]uint64{acc.GetAccountNumber()},
		[]uint64{acc.GetSequence()},
		priv,
	)
	require.NoError(t, err)
	bz, err := testApp.TxConfig().TxEncoder()(tx)
	require.NoError(t, err)
	return bz
}

func deliverWarmKeyGasTx(t *testing.T, testApp *app.App, signer sdk.AccAddress, priv cryptotypes.PrivKey, at time.Time, gas uint64, msgs ...sdk.Msg) *abci.ExecTxResult {
	t.Helper()
	bz := signWarmKeyTx(t, testApp, signer, priv, gas, msgs...)
	resp, err := testApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: testApp.LastBlockHeight() + 1,
		Time:   at,
		Txs:    [][]byte{bz},
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.TxResults)
	require.Equal(t, uint32(0), resp.TxResults[0].Code, resp.TxResults[0].Log)
	_, err = testApp.Commit()
	require.NoError(t, err)
	return resp.TxResults[0]
}

func commitWarmKeyGasBlock(t *testing.T, testApp *app.App, at time.Time) {
	t.Helper()
	_, err := testApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: testApp.LastBlockHeight() + 1,
		Time:   at,
	})
	require.NoError(t, err)
	_, err = testApp.Commit()
	require.NoError(t, err)
}

func warmKeyGasGroupPolicy(t *testing.T, testApp *app.App, admin sdk.AccAddress) sdk.AccAddress {
	t.Helper()
	msg := &group.MsgCreateGroupWithPolicy{
		Admin: admin.String(),
		Members: []group.MemberRequest{{
			Address: admin.String(),
			Weight:  "1",
		}},
	}
	require.NoError(t, msg.SetDecisionPolicy(group.NewThresholdDecisionPolicy("1", time.Hour, 0)))
	resp, err := testApp.GroupKeeper.CreateGroupWithPolicy(warmKeyGasCtx(testApp), msg)
	require.NoError(t, err)
	return sdk.MustAccAddressFromBech32(resp.GroupPolicyAddress)
}

func warmKeyGasCtx(testApp *app.App) sdk.Context {
	return testApp.NewUncachedContext(false, cmtproto.Header{
		Height:  testApp.LastBlockHeight(),
		ChainID: TallyTestChainID,
		Time:    time.Now().UTC(),
	})
}
