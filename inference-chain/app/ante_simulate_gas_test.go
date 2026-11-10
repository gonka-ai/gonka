package app

import (
	"errors"
	"testing"
	"time"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	"cosmossdk.io/store"
	storemetrics "cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	protov2 "google.golang.org/protobuf/proto"
)

func TestCountTXSimulateGasDecorator_SimulateMetersWithoutAssigningCounter(t *testing.T) {
	keyWasm := storetypes.NewKVStoreKey(wasmtypes.StoreKey)
	db := dbm.NewMemDB()
	ms := store.NewCommitMultiStore(db, log.NewTestLogger(t), storemetrics.NewNoOpMetrics())
	ms.MountStoreWithDB(keyWasm, storetypes.StoreTypeIAVL, db)
	require.NoError(t, ms.LoadLatestVersion())

	ctx := sdk.NewContext(ms.CacheMultiStore(), cmtproto.Header{Height: 100}, false, log.NewNopLogger()).
		WithGasMeter(storetypes.NewInfiniteGasMeter())
	dec := NewCountTXSimulateGasDecorator(runtime.NewKVStoreService(keyWasm))

	nextCalled := false
	_, err := dec.AnteHandle(ctx, testFeeTx{}, true, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		_, ok := wasmtypes.TXCounter(ctx)
		require.False(t, ok, "Simulate must not set CosmWasm env.transaction")
		require.True(t, ctx.MultiStore().GetKVStore(keyWasm).Has(wasmtypes.TXCounterPrefix))
		return ctx, nil
	})
	require.NoError(t, err)
	require.True(t, nextCalled)
	require.Greater(t, ctx.GasMeter().GasConsumed(), storetypes.Gas(0))
}

func TestCountTXSimulateGasDecorator_CheckTxAssignsCounter(t *testing.T) {
	keyWasm := storetypes.NewKVStoreKey(wasmtypes.StoreKey)
	db := dbm.NewMemDB()
	ms := store.NewCommitMultiStore(db, log.NewTestLogger(t), storemetrics.NewNoOpMetrics())
	ms.MountStoreWithDB(keyWasm, storetypes.StoreTypeIAVL, db)
	require.NoError(t, ms.LoadLatestVersion())

	const height int64 = 100
	ctx := sdk.NewContext(ms.CacheMultiStore(), cmtproto.Header{Height: height}, true, log.NewNopLogger())
	dec := NewCountTXSimulateGasDecorator(runtime.NewKVStoreService(keyWasm))

	_, err := dec.AnteHandle(ctx, testFeeTx{}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		got, ok := wasmtypes.TXCounter(ctx)
		require.True(t, ok)
		require.Equal(t, uint32(0), got)
		return ctx, nil
	})
	require.NoError(t, err)
}

type recordingNonceAdder struct {
	calls int
	err   error
}

func (r *recordingNonceAdder) TryAddUnorderedNonce(sdk.Context, []byte, time.Time) error {
	r.calls++
	return r.err
}

type unorderedSimTestTx struct {
	unordered bool
	timeout   time.Time
	signers   [][]byte
}

func (t unorderedSimTestTx) GetMsgs() []sdk.Msg                    { return nil }
func (t unorderedSimTestTx) GetMsgsV2() ([]protov2.Message, error) { return nil, nil }
func (t unorderedSimTestTx) GetUnordered() bool                    { return t.unordered }
func (t unorderedSimTestTx) GetTimeoutTimeStamp() time.Time        { return t.timeout }
func (t unorderedSimTestTx) GetSigners() ([][]byte, error)         { return t.signers, nil }
func (t unorderedSimTestTx) GetPubKeys() ([]types.PubKey, error)   { return nil, nil }
func (t unorderedSimTestTx) GetSignaturesV2() ([]signing.SignatureV2, error) {
	return nil, nil
}

func TestUnorderedNonceSimGasDecorator_SimulateMetersAndIgnoresDuplicate(t *testing.T) {
	ak := &recordingNonceAdder{err: errors.New("sender has already used timeout")}
	dec := NewUnorderedNonceSimGasDecorator(ak)
	ctx := newTestContext()
	tx := unorderedSimTestTx{
		unordered: true,
		timeout:   time.Unix(10, 0),
		signers:   [][]byte{[]byte("signer-a"), []byte("signer-b")},
	}

	nextCalled := false
	_, err := dec.AnteHandle(ctx, tx, true, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	require.NoError(t, err)
	require.True(t, nextCalled)
	require.Equal(t, 2, ak.calls)
}

func TestUnorderedNonceSimGasDecorator_SkipsWhenNotSimulate(t *testing.T) {
	ak := &recordingNonceAdder{}
	dec := NewUnorderedNonceSimGasDecorator(ak)
	tx := unorderedSimTestTx{unordered: true, timeout: time.Unix(10, 0), signers: [][]byte{[]byte("s")}}

	_, err := dec.AnteHandle(newTestContext(), tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		return ctx, nil
	})
	require.NoError(t, err)
	require.Equal(t, 0, ak.calls)
}

func TestUnorderedNonceSimGasDecorator_SkipsOrderedTx(t *testing.T) {
	ak := &recordingNonceAdder{}
	dec := NewUnorderedNonceSimGasDecorator(ak)
	tx := unorderedSimTestTx{unordered: false, timeout: time.Unix(10, 0), signers: [][]byte{[]byte("s")}}

	_, err := dec.AnteHandle(newTestContext(), tx, true, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		return ctx, nil
	})
	require.NoError(t, err)
	require.Equal(t, 0, ak.calls)
}
