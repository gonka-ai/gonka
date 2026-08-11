package app_test

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// Tests for #1539: the network-duty fee bypass waives fees for ~12 message
// types based on the Go type alone, and it runs before signature verification.
// The real authorization lives in the message handlers, which SDK 0.53 only
// runs in DeliverTx — after mempool admission and block inclusion. These tests
// prove the wired ante chain (not the decorator in isolation) now rejects
// duty-typed transactions from unauthorized actors at CheckTx.

type dutyCheckTxFixture struct {
	testApp *app.App
	// participant is a registered participant: the authorized duty actor.
	participant sdk.AccAddress
	partPriv    cryptotypes.PrivKey
	// outsider is funded but never registered: the attacker in the report.
	outsider     sdk.AccAddress
	outsiderPriv cryptotypes.PrivKey
	// grantee holds an authz grant from participant, modelling the DAPI's
	// warm key.
	grantee     sdk.AccAddress
	granteePriv cryptotypes.PrivKey
}

const dutyEpochIndex = uint64(400)

func setupDutyCheckTx(t *testing.T) dutyCheckTxFixture {
	t.Helper()
	testApp := createTestApp(t)

	ctx := testApp.NewUncachedContext(false, cmtproto.Header{
		Height:  testApp.LastBlockHeight(),
		ChainID: TallyTestChainID,
		Time:    time.Now().UTC(),
	})

	partPriv := secp256k1.GenPrivKey()
	participant := sdk.AccAddress(partPriv.PubKey().Address())
	outsiderPriv := secp256k1.GenPrivKey()
	outsider := sdk.AccAddress(outsiderPriv.PubKey().Address())
	granteePriv := secp256k1.GenPrivKey()
	grantee := sdk.AccAddress(granteePriv.PubKey().Address())

	// All three are funded: the report's point is that funding is not
	// authorization, so a funded outsider must still be rejected.
	fundAccount(t, ctx, testApp, participant, partPriv.PubKey())
	fundAccount(t, ctx, testApp, outsider, outsiderPriv.PubKey())
	fundAccount(t, ctx, testApp, grantee, granteePriv.PubKey())

	require.NoError(t, testApp.InferenceKeeper.SetParticipant(ctx, inferencetypes.Participant{
		Index:   participant.String(),
		Address: participant.String(),
		Weight:  10,
	}))
	require.NoError(t, testApp.InferenceKeeper.SetEffectiveEpochIndex(ctx, dutyEpochIndex))

	// Grant the warm key authority over MsgClaimRewards on behalf of the
	// participant, mirroring GrantMLOperationalKeyPermissionsToAccount.
	expiry := time.Now().UTC().Add(365 * 24 * time.Hour)
	require.NoError(t, testApp.AuthzKeeper.SaveGrant(
		ctx, grantee, participant,
		authztypes.NewGenericAuthorization(sdk.MsgTypeURL(&inferencetypes.MsgClaimRewards{})),
		&expiry,
	))

	_, err := testApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: testApp.LastBlockHeight() + 1,
		Time:   time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = testApp.Commit()
	require.NoError(t, err)

	return dutyCheckTxFixture{
		testApp:      testApp,
		participant:  participant,
		partPriv:     partPriv,
		outsider:     outsider,
		outsiderPriv: outsiderPriv,
		grantee:      grantee,
		granteePriv:  granteePriv,
	}
}

// runDutyCheckTx submits msgs with a zero fee, the shape the report describes.
func runDutyCheckTx(t *testing.T, f dutyCheckTxFixture, msgs []sdk.Msg, signer sdk.AccAddress, signerPriv cryptotypes.PrivKey) *abci.ResponseCheckTx {
	t.Helper()
	checkCtx := f.testApp.NewContext(true)
	acc := f.testApp.AccountKeeper.GetAccount(checkCtx, signer)
	require.NotNil(t, acc)

	tx, err := simtestutil.GenSignedMockTx(
		rand.New(rand.NewSource(1)),
		f.testApp.TxConfig(),
		msgs,
		sdk.NewCoins(), // zero fee: admitted only via the duty bypass
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
	return resp
}

func claimRewardsFor(addr sdk.AccAddress) *inferencetypes.MsgClaimRewards {
	// EpochIndex must be > 0 to pass ValidateBasic.
	return &inferencetypes.MsgClaimRewards{
		Creator:    addr.String(),
		EpochIndex: dutyEpochIndex - 1,
		Seed:       1,
	}
}

// TestNetworkDuty_CheckTx_RejectsUnregisteredActor is the core #1539
// regression: a structurally valid, zero-fee MsgClaimRewards from a funded but
// unregistered account used to return code 0 and occupy block space for free.
func TestNetworkDuty_CheckTx_RejectsUnregisteredActor(t *testing.T) {
	f := setupDutyCheckTx(t)

	resp := runDutyCheckTx(t, f, []sdk.Msg{claimRewardsFor(f.outsider)}, f.outsider, f.outsiderPriv)

	require.NotEqual(t, uint32(0), resp.Code,
		"CheckTx must reject a zero-fee duty tx from an unregistered actor (got code=0 log=%q)", resp.Log)
	require.True(t,
		strings.Contains(strings.ToLower(resp.Log), strings.ToLower(inferencetypes.ErrParticipantNotFound.Error())),
		"log must carry the registered ErrParticipantNotFound message; got code=%d log=%q", resp.Code, resp.Log)
}

// TestNetworkDuty_CheckTx_AdmitsRegisteredActor guards against over-rejection:
// the legitimate zero-fee path must keep working, otherwise the fix would be a
// liveness bug for consensus-critical duty traffic.
func TestNetworkDuty_CheckTx_AdmitsRegisteredActor(t *testing.T) {
	f := setupDutyCheckTx(t)

	resp := runDutyCheckTx(t, f, []sdk.Msg{claimRewardsFor(f.participant)}, f.participant, f.partPriv)

	require.Equal(t, uint32(0), resp.Code,
		"registered participant must still be admitted with zero fee; log=%q", resp.Log)
}

// TestNetworkDuty_CheckTx_MsgExec_RejectsForgedCreator closes the hole the
// direct-path check alone leaves open. Inside authz MsgExec the signature only
// proves the grantee signed, so an attacker could name a registered participant
// as Creator and inherit its exemption. The ante layer therefore also requires
// the authz grant that DeliverTx would require.
func TestNetworkDuty_CheckTx_MsgExec_RejectsForgedCreator(t *testing.T) {
	f := setupDutyCheckTx(t)

	// outsider wraps a claim whose Creator is the *registered* participant.
	// outsider holds no grant from participant.
	execMsg := authztypes.NewMsgExec(f.outsider, []sdk.Msg{claimRewardsFor(f.participant)})
	resp := runDutyCheckTx(t, f, []sdk.Msg{&execMsg}, f.outsider, f.outsiderPriv)

	require.NotEqual(t, uint32(0), resp.Code,
		"CheckTx must reject MsgExec naming another account as Creator without a grant (got code=0 log=%q)", resp.Log)
	require.True(t,
		strings.Contains(strings.ToLower(resp.Log), strings.ToLower(authztypes.ErrNoAuthorizationFound.Error())),
		"log must report the missing authz grant; got code=%d log=%q", resp.Code, resp.Log)
}

// TestNetworkDuty_CheckTx_MsgExec_AdmitsGrantedWarmKey is the production
// warm-key path: the DAPI wraps duty messages in MsgExec signed by the grantee
// while Creator names the cold account (tx_manager.go
// broadcastMessagesAtAttempt, cosmosclient.go setting Creator = icc.Address).
// This must not regress — it is why the check reads the message body rather
// than the tx signer.
func TestNetworkDuty_CheckTx_MsgExec_AdmitsGrantedWarmKey(t *testing.T) {
	f := setupDutyCheckTx(t)

	execMsg := authztypes.NewMsgExec(f.grantee, []sdk.Msg{claimRewardsFor(f.participant)})
	resp := runDutyCheckTx(t, f, []sdk.Msg{&execMsg}, f.grantee, f.granteePriv)

	require.Equal(t, uint32(0), resp.Code,
		"granted warm key acting for a registered participant must be admitted; log=%q", resp.Log)
}

// TestNetworkDuty_CheckTx_NonDutyUnaffected confirms the decorator is scoped to
// duty types and does not touch ordinary traffic.
func TestNetworkDuty_CheckTx_NonDutyUnaffected(t *testing.T) {
	f := setupDutyCheckTx(t)

	// MsgCreateDevshardEscrow is deliberately not a fee-exempt duty, so the
	// duty signer check must not be the thing that decides its fate.
	msg := &inferencetypes.MsgCreateDevshardEscrow{Creator: f.outsider.String()}
	resp := runDutyCheckTx(t, f, []sdk.Msg{msg}, f.outsider, f.outsiderPriv)

	require.NotContains(t, strings.ToLower(resp.Log), "network duty",
		"non-duty messages must not be rejected by the duty signer check; log=%q", resp.Log)
}
