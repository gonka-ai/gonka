package app_test

import (
	"context"
	"testing"
	"time"

	"cosmossdk.io/x/feegrant"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsign "github.com/cosmos/cosmos-sdk/x/auth/signing"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
)

type authTxOpts struct {
	unordered bool
	granter   sdk.AccAddress
}

// signAuthTx builds a memo-less tx shaped like the dapi's: optionally unordered and with a fee granter.
func signAuthTx(t *testing.T, a *app.App, msgs []sdk.Msg, signer sdk.AccAddress, priv cryptotypes.PrivKey, o authTxOpts) []byte {
	t.Helper()
	acc := a.AccountKeeper.GetAccount(a.NewContext(true), signer)
	require.NotNil(t, acc)
	seq := acc.GetSequence()
	if o.unordered {
		seq = 0
	}
	txCfg := a.TxConfig()
	signMode, err := authsign.APISignModeToInternal(txCfg.SignModeHandler().DefaultMode())
	require.NoError(t, err)
	sig := signing.SignatureV2{PubKey: priv.PubKey(), Data: &signing.SingleSignatureData{SignMode: signMode}, Sequence: seq}

	b := txCfg.NewTxBuilder()
	require.NoError(t, b.SetMsgs(msgs...))
	require.NoError(t, b.SetSignatures(sig))
	b.SetGasLimit(1_000_000)
	if o.unordered {
		b.SetUnordered(true)
		b.SetTimeoutTimestamp(time.Now().UTC().Add(5 * time.Minute))
	}
	if o.granter != nil {
		b.SetFeeGranter(o.granter)
	}
	signBytes, err := authsign.GetSignBytesAdapter(context.Background(), txCfg.SignModeHandler(), signMode, authsign.SignerData{
		Address: signer.String(), ChainID: TallyTestChainID, AccountNumber: acc.GetAccountNumber(), Sequence: seq, PubKey: priv.PubKey(),
	}, b.GetTx())
	require.NoError(t, err)
	sig.Data.(*signing.SingleSignatureData).Signature, err = priv.Sign(signBytes)
	require.NoError(t, err)
	require.NoError(t, b.SetSignatures(sig))
	bz, err := txCfg.TxEncoder()(b.GetTx())
	require.NoError(t, err)
	return bz
}

// grantFees lets payer pay fees for signer, as a host's cold key does for its warm key.
func grantFees(t *testing.T, a *app.App, payer, signer sdk.AccAddress) {
	t.Helper()
	ctx := a.NewUncachedContext(false, cmtproto.Header{Height: a.LastBlockHeight(), ChainID: TallyTestChainID, Time: time.Now().UTC()})
	require.NoError(t, a.FeeGrantKeeper.GrantAllowance(ctx, payer, signer, &feegrant.BasicAllowance{}))
	finalizeTxs(t, a)
}

func finalizeTxs(t *testing.T, a *app.App, txs ...[]byte) {
	t.Helper()
	resp, err := a.FinalizeBlock(&abci.RequestFinalizeBlock{Height: a.LastBlockHeight() + 1, Time: time.Now().UTC(), Txs: txs})
	require.NoError(t, err)
	for i, r := range resp.TxResults {
		require.Equal(t, uint32(0), r.Code, "tx %d log=%q", i, r.Log)
	}
	_, err = a.Commit()
	require.NoError(t, err)
}

// The per-tx account cache in ante must keep sequences right across txs of one
// signer and serve unordered, fee-granted txs like the dapi's.
func TestAnteAuthCache_SequentialAndUnorderedGrantedTxs(t *testing.T) {
	f := setupMsgExecCheckTx(t, true)
	a := f.testApp
	start := a.AccountKeeper.GetAccount(a.NewContext(true), f.granter).GetSequence()

	for i := 0; i < 3; i++ {
		bz := signAuthTx(t, a, []sdk.Msg{f.seedMsg}, f.granter, f.granterKey, authTxOpts{})
		resp, err := a.CheckTx(&abci.RequestCheckTx{Tx: bz})
		require.NoError(t, err)
		require.Equal(t, uint32(0), resp.Code, "CheckTx %d log=%q", i, resp.Log)
		finalizeTxs(t, a, bz)
	}
	require.Equal(t, start+3, a.AccountKeeper.GetAccount(a.NewContext(true), f.granter).GetSequence())

	grantFees(t, a, f.grantee, f.granter)
	bz := signAuthTx(t, a, []sdk.Msg{f.seedMsg}, f.granter, f.granterKey, authTxOpts{unordered: true, granter: f.grantee})
	resp, err := a.CheckTx(&abci.RequestCheckTx{Tx: bz})
	require.NoError(t, err)
	require.Equal(t, uint32(0), resp.Code, "unordered granted CheckTx log=%q", resp.Log)
	finalizeTxs(t, a, bz)
	require.Equal(t, start+3, a.AccountKeeper.GetAccount(a.NewContext(true), f.granter).GetSequence(), "unordered tx keeps the sequence")
}

// The tx position counter must stay out of the persistent wasm store.
func TestCountTX_NoPersistentCounterWrite(t *testing.T) {
	f := setupMsgExecCheckTx(t, true)
	a := f.testApp
	bz := signAuthTx(t, a, []sdk.Msg{f.seedMsg}, f.granter, f.granterKey, authTxOpts{})
	resp, err := a.FinalizeBlock(&abci.RequestFinalizeBlock{Height: a.LastBlockHeight() + 1, Time: time.Now().UTC(), Txs: [][]byte{bz}})
	require.NoError(t, err)
	require.Equal(t, uint32(0), resp.TxResults[0].Code, resp.TxResults[0].Log)
	t.Logf("seed tx gas used: %d", resp.TxResults[0].GasUsed)
	_, err = a.Commit()
	require.NoError(t, err)
	require.False(t, a.NewContext(true).KVStore(a.GetKey(wasmtypes.StoreKey)).Has(wasmtypes.TXCounterPrefix))
}
