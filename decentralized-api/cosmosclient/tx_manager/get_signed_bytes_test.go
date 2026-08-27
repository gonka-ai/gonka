package tx_manager

import (
	"context"
	"testing"
	"time"

	"decentralized-api/apiconfig"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient/mocks"
	testutil "github.com/productscience/inference/testutil/cosmoclient"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

const (
	signedBytesNetwork     = "cosmos"
	signedBytesAccountName = "cosmosaccount"
	signedBytesMnemonic    = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	signedBytesPassphrase  = "testpass"
	signedBytesTimeoutH    = uint64(250)
)

func newSigningManager(t *testing.T, authzMode bool) *manager {
	t.Helper()

	rpc := mocks.NewRPCClient(t)
	client := testutil.NewMockClient(t, rpc, signedBytesNetwork, signedBytesAccountName, signedBytesMnemonic, signedBytesPassphrase)
	authztypes.RegisterInterfaces(client.Context().InterfaceRegistry)

	signer, err := client.AccountRegistry.GetByName(signedBytesAccountName)
	require.NoError(t, err)
	signerPub, err := signer.Record.GetPubKey()
	require.NoError(t, err)

	accountKey := signerPub
	if authzMode {
		cold, _, err := client.AccountRegistry.Create("cold")
		require.NoError(t, err)
		coldPub, err := cold.Record.GetPubKey()
		require.NoError(t, err)
		accountKey = coldPub
	}

	factory := client.TxFactory.
		WithAccountNumber(1).
		WithGas(0).
		WithUnordered(true).
		WithKeybase(client.AccountRegistry.Keyring)

	return &manager{
		ctx:    context.Background(),
		client: &client,
		apiAccount: &apiconfig.ApiAccount{
			AccountKey:    accountKey,
			SignerAccount: &signer,
			AddressPrefix: signedBytesNetwork,
		},
		txFactory:         &factory,
		defaultTimeout:    30 * time.Second,
		minGasPriceNgonka: 0,
		feeTree:           newFeeTreeCache(),
		blockTimeTracker: &blockTimeTracker{
			latestBlockTime: time.Unix(1_700_000_000, 0),
			lastUpdatedAt:   time.Now(),
		},
	}
}

func storeCommitForTest(creator string) *types.MsgPoCV2StoreCommit {
	return &types.MsgPoCV2StoreCommit{
		Creator:                  creator,
		PocStageStartBlockHeight: 100,
		Entries: []*types.PoCV2CommitEntry{
			{ModelId: "m1", Count: 1, RootHash: []byte{1}},
		},
	}
}

// signLikeBroadcast mirrors broadcastMessagesAtAttemptWithOpts: wrap authz
// before BuildUnsignedTx, then getSignedBytes on the outer body.
func signLikeBroadcast(t *testing.T, m *manager, msgs []sdk.Msg, timeoutHeight uint64) ([]byte, time.Time) {
	t.Helper()

	factory, err := m.getFactory("signed-bytes-test")
	require.NoError(t, err)

	finalMsgs := msgs
	if !m.apiAccount.IsSignerTheMainAccount() {
		grantee, err := m.apiAccount.SignerAddress()
		require.NoError(t, err)
		exec := authztypes.NewMsgExec(grantee, msgs)
		finalMsgs = []sdk.Msg{&exec}
	}

	unsignedTx, err := factory.BuildUnsignedTx(finalMsgs...)
	require.NoError(t, err)

	txBytes, ts, err := m.getSignedBytes("signed-bytes-test", unsignedTx, factory, 250_000, msgs, timeoutHeight)
	require.NoError(t, err)
	require.False(t, ts.IsZero())
	return txBytes, ts
}

func decodeSignedTx(t *testing.T, m *manager, txBytes []byte) sdk.Tx {
	t.Helper()
	decoded, err := m.client.Context().TxConfig.TxDecoder()(txBytes)
	require.NoError(t, err)
	return decoded
}

func assertTimeoutBody(t *testing.T, decoded sdk.Tx, wantHeight uint64, wantTs time.Time) {
	t.Helper()

	th, ok := decoded.(sdk.TxWithTimeoutHeight)
	require.True(t, ok, "decoded tx must implement TxWithTimeoutHeight")
	require.Equal(t, wantHeight, th.GetTimeoutHeight())

	unord, ok := decoded.(sdk.TxWithUnordered)
	require.True(t, ok, "decoded tx must implement TxWithUnordered")
	require.True(t, unord.GetUnordered())
	gotTs := unord.GetTimeoutTimeStamp()
	require.False(t, gotTs.IsZero())
	require.Equal(t, wantTs.UnixNano(), gotTs.UnixNano())
}

func TestTimeoutTimestamp_RefreshesStaleCacheFromStatus(t *testing.T) {
	stale := time.Unix(1_700_000_000, 0)
	fresh := time.Now().UTC().Truncate(time.Second)

	rpc := mocks.NewRPCClient(t)
	client := testutil.NewMockClient(t, rpc, signedBytesNetwork, signedBytesAccountName, signedBytesMnemonic, signedBytesPassphrase)
	rpc.EXPECT().Status(context.Background()).Return(&ctypes.ResultStatus{
		SyncInfo: ctypes.SyncInfo{
			LatestBlockTime:   fresh,
			LatestBlockHeight: 123,
		},
	}, nil).Once()

	m := &manager{
		ctx:            context.Background(),
		client:         &client,
		defaultTimeout: 30 * time.Second,
		blockTimeTracker: &blockTimeTracker{
			latestBlockTime: stale,
			lastUpdatedAt:   time.Unix(1, 0),
			maxBlockTimeout: 10 * time.Second,
		},
	}
	got, err := m.timeoutTimestamp()
	require.NoError(t, err)
	require.Equal(t, fresh, m.blockTimeTracker.latestBlockTime)
	require.True(t, !got.Before(fresh.Add(30*time.Second)))
}

func TestGetSignedBytes_DirectStoreCommitSetsTimeoutHeight(t *testing.T) {
	m := newSigningManager(t, false)
	require.True(t, m.apiAccount.IsSignerTheMainAccount())

	creator, err := m.apiAccount.AccountAddressBech32()
	require.NoError(t, err)
	msg := storeCommitForTest(creator)

	txBytes, ts := signLikeBroadcast(t, m, []sdk.Msg{msg}, signedBytesTimeoutH)
	decoded := decodeSignedTx(t, m, txBytes)
	assertTimeoutBody(t, decoded, signedBytesTimeoutH, ts)

	msgs := decoded.GetMsgs()
	require.Len(t, msgs, 1)
	_, ok := msgs[0].(*types.MsgPoCV2StoreCommit)
	require.True(t, ok, "direct path must encode StoreCommit, not MsgExec")
}

func TestGetSignedBytes_AuthzExecKeepsOuterTimeoutHeight(t *testing.T) {
	m := newSigningManager(t, true)
	require.False(t, m.apiAccount.IsSignerTheMainAccount())

	creator, err := m.apiAccount.AccountAddressBech32()
	require.NoError(t, err)
	msg := storeCommitForTest(creator)

	txBytes, ts := signLikeBroadcast(t, m, []sdk.Msg{msg}, signedBytesTimeoutH)
	decoded := decodeSignedTx(t, m, txBytes)
	assertTimeoutBody(t, decoded, signedBytesTimeoutH, ts)

	msgs := decoded.GetMsgs()
	require.Len(t, msgs, 1)
	exec, ok := msgs[0].(*authztypes.MsgExec)
	require.True(t, ok, "warm-key path must wrap StoreCommit in MsgExec")

	inner, err := exec.GetMessages()
	require.NoError(t, err)
	require.Len(t, inner, 1)
	_, ok = inner[0].(*types.MsgPoCV2StoreCommit)
	require.True(t, ok, "MsgExec inner message must be StoreCommit")
}

func TestGetSignedBytes_ZeroTimeoutHeightStaysUnset(t *testing.T) {
	m := newSigningManager(t, false)
	creator, err := m.apiAccount.AccountAddressBech32()
	require.NoError(t, err)

	msg := &types.MsgClaimRewards{
		Creator:    creator,
		Seed:       1,
		EpochIndex: 1,
	}
	txBytes, ts := signLikeBroadcast(t, m, []sdk.Msg{msg}, 0)
	decoded := decodeSignedTx(t, m, txBytes)
	assertTimeoutBody(t, decoded, 0, ts)

	msgs := decoded.GetMsgs()
	require.Len(t, msgs, 1)
	_, ok := msgs[0].(*types.MsgClaimRewards)
	require.True(t, ok)
}
