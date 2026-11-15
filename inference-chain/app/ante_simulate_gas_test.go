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

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	protov2 "google.golang.org/protobuf/proto"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

func newCountTXTestStore(t *testing.T) (storetypes.CommitMultiStore, *storetypes.TransientStoreKey) {
	key := storetypes.NewTransientStoreKey(inferencetypes.TransientStoreKey)
	db := dbm.NewMemDB()
	ms := store.NewCommitMultiStore(db, log.NewTestLogger(t), storemetrics.NewNoOpMetrics())
	ms.MountStoreWithDB(key, storetypes.StoreTypeTransient, nil)
	require.NoError(t, ms.LoadLatestVersion())
	return ms, key
}

func TestCountTXDecorator_SimulateMetersWithoutAssigningCounter(t *testing.T) {
	ms, key := newCountTXTestStore(t)
	ctx := sdk.NewContext(ms.CacheMultiStore(), cmtproto.Header{Height: 100}, false, log.NewNopLogger()).
		WithGasMeter(storetypes.NewInfiniteGasMeter())
	dec := NewCountTXDecorator(key)

	nextCalled := false
	_, err := dec.AnteHandle(ctx, testFeeTx{}, true, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		_, ok := wasmtypes.TXCounter(ctx)
		require.False(t, ok, "Simulate must not set CosmWasm env.transaction")
		require.True(t, ctx.MultiStore().GetKVStore(key).Has(inferencetypes.TransientTxCounterKey))
		return ctx, nil
	})
	require.NoError(t, err)
	require.True(t, nextCalled)
	require.Greater(t, ctx.GasMeter().GasConsumed(), storetypes.Gas(0))
}

// Counter is the tx position in the block and restarts after Commit, as in wasmd.
func TestCountTXDecorator_CountsPerBlock(t *testing.T) {
	ms, key := newCountTXTestStore(t)
	dec := NewCountTXDecorator(key)
	deliver := func(height int64) uint32 {
		cache := ms.CacheMultiStore()
		ctx := sdk.NewContext(cache, cmtproto.Header{Height: height}, false, log.NewNopLogger())
		var got uint32
		_, err := dec.AnteHandle(ctx, testFeeTx{}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
			v, ok := wasmtypes.TXCounter(ctx)
			require.True(t, ok)
			got = v
			return ctx, nil
		})
		require.NoError(t, err)
		cache.Write()
		return got
	}
	require.Equal(t, uint32(0), deliver(100))
	require.Equal(t, uint32(1), deliver(100))
	require.Equal(t, uint32(2), deliver(100))
	ms.Commit()
	require.Equal(t, uint32(0), deliver(101))
	require.Equal(t, uint32(1), deliver(101))
}

// Gas per tx: wasmd's persistent counter (Get+Set of a 12-byte value in the
// wasm KV store) against the transient counter.
func TestCountTXDecorator_Gas(t *testing.T) {
	ms, key := newCountTXTestStore(t)
	ctx := sdk.NewContext(ms.CacheMultiStore(), cmtproto.Header{Height: 100}, false, log.NewNopLogger()).
		WithGasMeter(storetypes.NewInfiniteGasMeter())
	noop := func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) { return ctx, nil }
	_, err := NewCountTXDecorator(key).AnteHandle(ctx, testFeeTx{}, false, noop)
	require.NoError(t, err)
	transient := ctx.GasMeter().GasConsumed()

	keyWasm := storetypes.NewKVStoreKey(wasmtypes.StoreKey)
	db := dbm.NewMemDB()
	wms := store.NewCommitMultiStore(db, log.NewTestLogger(t), storemetrics.NewNoOpMetrics())
	wms.MountStoreWithDB(keyWasm, storetypes.StoreTypeIAVL, db)
	require.NoError(t, wms.LoadLatestVersion())
	wctx := sdk.NewContext(wms.CacheMultiStore(), cmtproto.Header{Height: 100}, false, log.NewNopLogger()).
		WithGasMeter(storetypes.NewInfiniteGasMeter())
	// second tx of the block: the persistent key already holds (height, 1)
	wctx.KVStore(keyWasm).Set(wasmtypes.TXCounterPrefix, append(sdk.Uint64ToBigEndian(100), 0, 0, 0, 1))
	before := wctx.GasMeter().GasConsumed()
	_, err = wasmkeeper.NewCountTXDecorator(runtime.NewKVStoreService(keyWasm)).AnteHandle(wctx, testFeeTx{}, false, noop)
	require.NoError(t, err)
	persistent := wctx.GasMeter().GasConsumed() - before

	t.Logf("tx counter gas per tx: wasmd persistent %d, transient %d", persistent, transient)
	require.Less(t, transient, persistent)
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
