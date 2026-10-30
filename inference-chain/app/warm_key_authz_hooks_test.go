//go:build authz_grant_hooks

// This test needs authz Keeper.SetGrantHooks. That method is not in
// github.com/gonka-ai/cosmos-sdk v0.53.3-ps19-observability, and the call in
// app.go is commented out. Build with -tags authz_grant_hooks once the chain
// depends on a tag that provides it.
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
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	authz "github.com/cosmos/cosmos-sdk/x/authz"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

func TestWarmKeyLogFollowsAuthzWrites(t *testing.T) {
	testApp := createTestApp(t)
	adminKey := secp256k1.GenPrivKey()
	admin := sdk.AccAddress(adminKey.PubKey().Address())
	granterKey := secp256k1.GenPrivKey()
	granter := sdk.AccAddress(granterKey.PubKey().Address())
	ctx := warmKeyCtx(testApp)
	fundAccount(t, ctx, testApp, admin, adminKey.PubKey())
	fundAccount(t, ctx, testApp, granter, granterKey.PubKey())
	policy := warmKeyGroupPolicy(t, testApp, ctx, admin)
	commitWarmKeyBlock(t, testApp, time.Now().UTC())

	t.Run("transaction grant replace and revoke", func(t *testing.T) {
		grantee := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
		expires := time.Now().UTC().Add(24 * time.Hour)
		grant, err := authz.NewMsgGrant(granter, grantee, authz.NewGenericAuthorization(inferencetypes.WarmKeyGrantMarkerTypeURL), &expires)
		require.NoError(t, err)
		other, err := authz.NewMsgGrant(granter, grantee, authz.NewGenericAuthorization("/cosmos.bank.v1beta1.MsgSend"), nil)
		require.NoError(t, err)
		deliverWarmKeyTx(t, testApp, granter, granterKey, time.Now().UTC(), grant, other)

		entries := warmKeyEntries(t, testApp, granter)
		require.Len(t, entries, 1)
		require.Equal(t, grantee.String(), entries[0].Grantee)
		require.Equal(t, expires.UnixNano(), entries[0].ExpiresAtUnix)
		require.Equal(t, inferencetypes.WarmKeyLogEnd_WARM_KEY_LOG_END_NONE, entries[0].End)

		again, err := authz.NewMsgGrant(granter, grantee, authz.NewGenericAuthorization(inferencetypes.WarmKeyGrantMarkerTypeURL), nil)
		require.NoError(t, err)
		deliverWarmKeyTx(t, testApp, granter, granterKey, time.Now().UTC(), again)
		entries = warmKeyEntries(t, testApp, granter)
		require.Equal(t, inferencetypes.WarmKeyLogEnd_WARM_KEY_LOG_END_REPLACED, entries[0].End)
		require.Equal(t, inferencetypes.WarmKeyLogEnd_WARM_KEY_LOG_END_NONE, entries[1].End)

		revoke := authz.NewMsgRevoke(granter, grantee, inferencetypes.WarmKeyGrantMarkerTypeURL)
		deliverWarmKeyTx(t, testApp, granter, granterKey, time.Now().UTC(), &revoke)
		entries = warmKeyEntries(t, testApp, granter)
		require.Equal(t, inferencetypes.WarmKeyLogEnd_WARM_KEY_LOG_END_REVOKED, entries[1].End)
		require.Nil(t, warmKeyAuth(t, testApp, grantee, granter))
	})

	t.Run("msg exec", func(t *testing.T) {
		grantee := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
		grant, err := authz.NewMsgGrant(granter, grantee, authz.NewGenericAuthorization(inferencetypes.WarmKeyGrantMarkerTypeURL), nil)
		require.NoError(t, err)
		exec := authz.NewMsgExec(granter, []sdk.Msg{grant})
		got := deliverWarmKeyTx(t, testApp, granter, granterKey, time.Now().UTC(), &exec)
		require.True(t, hasABCIEvent(got.Events, "warm_key_granted"))
		entries := warmKeyEntries(t, testApp, granter)
		require.Equal(t, grantee.String(), entries[len(entries)-1].Grantee)
	})

	t.Run("group exec try", func(t *testing.T) {
		grantee := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
		grant, err := authz.NewMsgGrant(policy, grantee, authz.NewGenericAuthorization(inferencetypes.WarmKeyGrantMarkerTypeURL), nil)
		require.NoError(t, err)
		prop := warmKeyProposal(t, policy, admin, group.Exec_EXEC_TRY, grant)
		got := deliverWarmKeyTx(t, testApp, admin, adminKey, time.Now().UTC(), prop)
		require.True(t, hasABCIEvent(got.Events, "warm_key_granted"))
		require.NotNil(t, warmKeyAuth(t, testApp, grantee, policy))
		entries := warmKeyEntries(t, testApp, policy)
		require.Len(t, entries, 1)
		require.Equal(t, inferencetypes.WarmKeyLogEnd_WARM_KEY_LOG_END_NONE, entries[0].End)
	})

	t.Run("group exec in its own transaction", func(t *testing.T) {
		entries := warmKeyEntries(t, testApp, policy)
		require.Len(t, entries, 1)
		grantee, err := sdk.AccAddressFromBech32(entries[0].Grantee)
		require.NoError(t, err)
		revoke := authz.NewMsgRevoke(policy, grantee, inferencetypes.WarmKeyGrantMarkerTypeURL)
		prop := warmKeyProposal(t, policy, admin, group.Exec_EXEC_UNSPECIFIED, &revoke)
		resp, err := testApp.GroupKeeper.SubmitProposal(warmKeyCtx(testApp), prop)
		require.NoError(t, err)
		_, err = testApp.GroupKeeper.Vote(warmKeyCtx(testApp), &group.MsgVote{
			ProposalId: resp.ProposalId,
			Voter:      admin.String(),
			Option:     group.VOTE_OPTION_YES,
		})
		require.NoError(t, err)
		commitWarmKeyBlock(t, testApp, time.Now().UTC())

		deliverWarmKeyTx(t, testApp, admin, adminKey, time.Now().UTC(), &group.MsgExec{
			ProposalId: resp.ProposalId,
			Executor:   admin.String(),
		})
		require.Nil(t, warmKeyAuth(t, testApp, grantee, policy))
		entries = warmKeyEntries(t, testApp, policy)
		require.Equal(t, inferencetypes.WarmKeyLogEnd_WARM_KEY_LOG_END_REVOKED, entries[0].End)
	})

	t.Run("failed group execution leaves the log unchanged", func(t *testing.T) {
		grantee := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
		grant, err := authz.NewMsgGrant(policy, grantee, authz.NewGenericAuthorization(inferencetypes.WarmKeyGrantMarkerTypeURL), nil)
		require.NoError(t, err)
		missing := authz.NewMsgRevoke(policy, grantee, "/cosmos.bank.v1beta1.MsgSend")
		prop := warmKeyProposal(t, policy, admin, group.Exec_EXEC_UNSPECIFIED, grant, &missing)
		resp, err := testApp.GroupKeeper.SubmitProposal(warmKeyCtx(testApp), prop)
		require.NoError(t, err)
		_, err = testApp.GroupKeeper.Vote(warmKeyCtx(testApp), &group.MsgVote{
			ProposalId: resp.ProposalId,
			Voter:      admin.String(),
			Option:     group.VOTE_OPTION_YES,
		})
		require.NoError(t, err)
		commitWarmKeyBlock(t, testApp, time.Now().UTC())

		before := len(warmKeyEntries(t, testApp, policy))
		deliverWarmKeyTx(t, testApp, admin, adminKey, time.Now().UTC(), &group.MsgExec{
			ProposalId: resp.ProposalId,
			Executor:   admin.String(),
		})
		stored, err := testApp.GroupKeeper.Proposal(warmKeyCtx(testApp), &group.QueryProposalRequest{ProposalId: resp.ProposalId})
		require.NoError(t, err)
		require.Equal(t, group.PROPOSAL_EXECUTOR_RESULT_FAILURE, stored.Proposal.ExecutorResult)
		require.Nil(t, warmKeyAuth(t, testApp, grantee, policy))
		require.Len(t, warmKeyEntries(t, testApp, policy), before)
	})

	t.Run("gov module account through the message router", func(t *testing.T) {
		grantee := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
		govAddr := authtypes.NewModuleAddress(govtypes.ModuleName)
		grant, err := authz.NewMsgGrant(govAddr, grantee, authz.NewGenericAuthorization(inferencetypes.WarmKeyGrantMarkerTypeURL), nil)
		require.NoError(t, err)
		handler := testApp.MsgServiceRouter().Handler(grant)
		require.NotNil(t, handler)
		_, err = handler(warmKeyCtx(testApp), grant)
		require.NoError(t, err)
		require.NotNil(t, warmKeyAuth(t, testApp, grantee, govAddr))
		entries := warmKeyEntries(t, testApp, govAddr)
		require.Len(t, entries, 1)
		require.Equal(t, inferencetypes.WarmKeyLogEnd_WARM_KEY_LOG_END_NONE, entries[0].End)
	})

	t.Run("expiry deletes the authz grant and does not end the log", func(t *testing.T) {
		grantee := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
		expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
		require.NoError(t, testApp.AuthzKeeper.SaveGrant(
			warmKeyCtx(testApp), grantee, granter,
			authz.NewGenericAuthorization(inferencetypes.WarmKeyGrantMarkerTypeURL), &expires,
		))
		require.NotNil(t, warmKeyAuth(t, testApp, grantee, granter))
		resp := commitWarmKeyBlock(t, testApp, expires.Add(time.Second))
		require.False(t, hasABCIEvent(resp.Events, "warm_key_revoked"))
		require.Nil(t, warmKeyAuth(t, testApp, grantee, granter))
		var ended inferencetypes.WarmKeyLogEntry
		for _, entry := range warmKeyEntries(t, testApp, granter) {
			if entry.Grantee == grantee.String() && entry.ExpiresAtUnix == expires.UnixNano() {
				ended = entry
			}
		}
		require.Equal(t, expires.UnixNano(), ended.ExpiresAtUnix)
		require.Equal(t, inferencetypes.WarmKeyLogEnd_WARM_KEY_LOG_END_NONE, ended.End)
	})

	t.Run("direct save grant", func(t *testing.T) {
		holderKey := secp256k1.GenPrivKey()
		holder := sdk.AccAddress(holderKey.PubKey().Address())
		grantee := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
		ctx := warmKeyCtx(testApp)
		require.NoError(t, testApp.AuthzKeeper.SaveGrant(ctx, grantee, holder, authz.NewGenericAuthorization("/cosmos.bank.v1beta1.MsgSend"), nil))
		require.Empty(t, warmKeyEntries(t, testApp, holder))
		require.NoError(t, testApp.AuthzKeeper.SaveGrant(ctx, grantee, holder, authz.NewGenericAuthorization(inferencetypes.WarmKeyGrantMarkerTypeURL), nil))
		entries := warmKeyEntries(t, testApp, holder)
		require.Len(t, entries, 1)
		require.Equal(t, inferencetypes.WarmKeyLogEnd_WARM_KEY_LOG_END_NONE, entries[0].End)
	})
}

func warmKeyGroupPolicy(t *testing.T, testApp *app.App, ctx sdk.Context, admin sdk.AccAddress) sdk.AccAddress {
	t.Helper()
	msg := &group.MsgCreateGroupWithPolicy{
		Admin: admin.String(),
		Members: []group.MemberRequest{{
			Address: admin.String(),
			Weight:  "1",
		}},
	}
	require.NoError(t, msg.SetDecisionPolicy(group.NewThresholdDecisionPolicy("1", time.Hour, 0)))
	resp, err := testApp.GroupKeeper.CreateGroupWithPolicy(ctx, msg)
	require.NoError(t, err)
	return sdk.MustAccAddressFromBech32(resp.GroupPolicyAddress)
}

func warmKeyProposal(t *testing.T, policy, proposer sdk.AccAddress, exec group.Exec, msgs ...sdk.Msg) *group.MsgSubmitProposal {
	t.Helper()
	prop := &group.MsgSubmitProposal{
		GroupPolicyAddress: policy.String(),
		Proposers:          []string{proposer.String()},
		Exec:               exec,
		Title:              "warm key",
		Summary:            "warm key",
	}
	require.NoError(t, prop.SetMsgs(msgs))
	return prop
}

func deliverWarmKeyTx(t *testing.T, testApp *app.App, signer sdk.AccAddress, priv cryptotypes.PrivKey, at time.Time, msgs ...sdk.Msg) *abci.ExecTxResult {
	t.Helper()
	acc := testApp.AccountKeeper.GetAccount(testApp.NewContext(true), signer)
	require.NotNil(t, acc)
	tx, err := simtestutil.GenSignedMockTx(
		rand.New(rand.NewSource(1)),
		testApp.TxConfig(),
		msgs,
		sdk.NewCoins(sdk.NewCoin(inferencetypes.BaseCoin, math.NewInt(10_000_000))),
		simtestutil.DefaultGenTxGas,
		TallyTestChainID,
		[]uint64{acc.GetAccountNumber()},
		[]uint64{acc.GetSequence()},
		priv,
	)
	require.NoError(t, err)
	bz, err := testApp.TxConfig().TxEncoder()(tx)
	require.NoError(t, err)
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

func commitWarmKeyBlock(t *testing.T, testApp *app.App, at time.Time) *abci.ResponseFinalizeBlock {
	t.Helper()
	resp, err := testApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: testApp.LastBlockHeight() + 1,
		Time:   at,
	})
	require.NoError(t, err)
	_, err = testApp.Commit()
	require.NoError(t, err)
	return resp
}

func warmKeyCtx(testApp *app.App) sdk.Context {
	return testApp.NewUncachedContext(false, cmtproto.Header{
		Height:  testApp.LastBlockHeight(),
		ChainID: TallyTestChainID,
		Time:    time.Now().UTC(),
	})
}

func warmKeyEntries(t *testing.T, testApp *app.App, granter sdk.AccAddress) []inferencetypes.WarmKeyLogEntry {
	t.Helper()
	entries, err := testApp.InferenceKeeper.WarmKeyLogEntries(warmKeyCtx(testApp), granter)
	require.NoError(t, err)
	return entries
}

func warmKeyAuth(t *testing.T, testApp *app.App, grantee, granter sdk.AccAddress) authz.Authorization {
	t.Helper()
	auth, _ := testApp.AuthzKeeper.GetAuthorization(warmKeyCtx(testApp), grantee, granter, inferencetypes.WarmKeyGrantMarkerTypeURL)
	return auth
}

func hasABCIEvent(events []abci.Event, typ string) bool {
	for _, ev := range events {
		if ev.Type == typ {
			return true
		}
	}
	return false
}
