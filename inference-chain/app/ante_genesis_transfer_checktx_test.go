package app_test

import (
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/stretchr/testify/require"

	genesistransfertypes "github.com/productscience/inference/x/genesistransfer/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

func setGenesisTransferAllowList(t *testing.T, f msgExecCheckTxFixture, allowed ...string) {
	t.Helper()
	ctx := f.testApp.NewUncachedContext(false, cmtproto.Header{
		Height:  f.testApp.LastBlockHeight(),
		ChainID: TallyTestChainID,
		Time:    time.Now().UTC(),
	})
	params, err := f.testApp.GenesistransferKeeper.GetParams(ctx)
	require.NoError(t, err)
	params.RestrictToList = true
	params.AllowedAccounts = allowed
	require.NoError(t, f.testApp.GenesistransferKeeper.SetParams(ctx, params))
	_, err = f.testApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: f.testApp.LastBlockHeight() + 1, Time: time.Now().UTC()})
	require.NoError(t, err)
	_, err = f.testApp.Commit()
	require.NoError(t, err)
}

func transferOwnershipMsg(genesis sdk.AccAddress) *genesistransfertypes.MsgTransferOwnership {
	return &genesistransfertypes.MsgTransferOwnership{
		GenesisAddress:   genesis.String(),
		RecipientAddress: sdk.AccAddress(make([]byte, 20)).String(),
	}
}

// MsgTransferOwnership has no fee group, so a zero-fee one from an account
// outside allowed_accounts used to pass CheckTx and fail only in DeliverTx.
func TestGenesisTransfer_CheckTx_RejectsNotAllowedSigner(t *testing.T) {
	f := setupMsgExecCheckTx(t, false)
	setGenesisTransferAllowList(t, f, f.grantee.String())
	require.Equal(t, "", inferencetypes.FeeGroupOf(transferOwnershipMsg(f.granter)))

	_, resp := signCheckTx(t, f, []sdk.Msg{transferOwnershipMsg(f.granter)}, f.granter, f.granterKey)
	require.Equal(t, genesistransfertypes.ErrNotInAllowedList.ABCICode(), resp.Code, resp.Log)
	require.Equal(t, genesistransfertypes.ModuleName, resp.Codespace)
}

// Liveness: an allowlisted, funded genesis account is still admitted.
func TestGenesisTransfer_CheckTx_AdmitsAllowedSigner(t *testing.T) {
	f := setupMsgExecCheckTx(t, false)
	setGenesisTransferAllowList(t, f, f.granter.String())

	_, resp := signCheckTx(t, f, []sdk.Msg{transferOwnershipMsg(f.granter)}, f.granter, f.granterKey)
	require.Equal(t, uint32(0), resp.Code, resp.Log)
}

// The balance check stays in DeliverTx: an allowlisted account whose funds
// are still in the mempool must not be turned away at CheckTx.
func TestGenesisTransfer_CheckTx_AdmitsAllowedSignerWithoutBalance(t *testing.T) {
	f := setupMsgExecCheckTx(t, false)
	key := secp256k1.GenPrivKey()
	addr := sdk.AccAddress(key.PubKey().Address())
	ctx := f.testApp.NewUncachedContext(false, cmtproto.Header{
		Height:  f.testApp.LastBlockHeight(),
		ChainID: TallyTestChainID,
		Time:    time.Now().UTC(),
	})
	f.testApp.AccountKeeper.SetAccount(ctx, f.testApp.AccountKeeper.NewAccountWithAddress(ctx, addr))
	setGenesisTransferAllowList(t, f, addr.String())
	require.True(t, f.testApp.BankKeeper.GetAllBalances(f.testApp.NewContext(true), addr).IsZero())

	_, resp := signCheckTx(t, f, []sdk.Msg{transferOwnershipMsg(addr)}, addr, key)
	require.Equal(t, uint32(0), resp.Code, resp.Log)
}

// One transfer per account: a second one is rejected before the block.
func TestGenesisTransfer_CheckTx_RejectsAlreadyTransferred(t *testing.T) {
	f := setupMsgExecCheckTx(t, false)
	setGenesisTransferAllowList(t, f, f.granter.String())
	ctx := f.testApp.NewUncachedContext(false, cmtproto.Header{
		Height:  f.testApp.LastBlockHeight(),
		ChainID: TallyTestChainID,
		Time:    time.Now().UTC(),
	})
	require.NoError(t, f.testApp.GenesistransferKeeper.SetTransferRecord(ctx, genesistransfertypes.TransferRecord{
		GenesisAddress:   f.granter.String(),
		RecipientAddress: f.grantee.String(),
		TransferHeight:   1,
		Completed:        true,
	}))
	_, err := f.testApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: f.testApp.LastBlockHeight() + 1, Time: time.Now().UTC()})
	require.NoError(t, err)
	_, err = f.testApp.Commit()
	require.NoError(t, err)

	_, resp := signCheckTx(t, f, []sdk.Msg{transferOwnershipMsg(f.granter)}, f.granter, f.granterKey)
	require.Equal(t, genesistransfertypes.ErrAlreadyTransferred.ABCICode(), resp.Code, resp.Log)
}

// The same message inside MsgExec is unwrapped as fee classification does.
func TestGenesisTransfer_CheckTx_RejectsInsideMsgExec(t *testing.T) {
	f := setupMsgExecCheckTx(t, false)
	setGenesisTransferAllowList(t, f, f.grantee.String())

	inner, err := codectypes.NewAnyWithValue(transferOwnershipMsg(f.granter))
	require.NoError(t, err)
	exec := &authztypes.MsgExec{Grantee: f.granter.String(), Msgs: []*codectypes.Any{inner}}

	_, resp := signCheckTx(t, f, []sdk.Msg{exec}, f.granter, f.granterKey)
	require.Equal(t, genesistransfertypes.ErrNotInAllowedList.ABCICode(), resp.Code, resp.Log)
}

// An account that is both outside allowed_accounts and already transferred
// gets the handler's error: ValidateTransfer checks the record first.
func TestGenesisTransfer_CheckTx_AlreadyTransferredWinsOverAllowList(t *testing.T) {
	f := setupMsgExecCheckTx(t, false)
	setGenesisTransferAllowList(t, f, f.grantee.String())
	ctx := f.testApp.NewUncachedContext(false, cmtproto.Header{
		Height:  f.testApp.LastBlockHeight(),
		ChainID: TallyTestChainID,
		Time:    time.Now().UTC(),
	})
	require.NoError(t, f.testApp.GenesistransferKeeper.SetTransferRecord(ctx, genesistransfertypes.TransferRecord{
		GenesisAddress:   f.granter.String(),
		RecipientAddress: f.grantee.String(),
		TransferHeight:   1,
		Completed:        true,
	}))
	_, err := f.testApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: f.testApp.LastBlockHeight() + 1, Time: time.Now().UTC()})
	require.NoError(t, err)
	_, err = f.testApp.Commit()
	require.NoError(t, err)

	_, resp := signCheckTx(t, f, []sdk.Msg{transferOwnershipMsg(f.granter)}, f.granter, f.granterKey)
	require.Equal(t, genesistransfertypes.ErrAlreadyTransferred.ABCICode(), resp.Code, resp.Log)
}
