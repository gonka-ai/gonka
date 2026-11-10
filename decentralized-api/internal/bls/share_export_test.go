package bls

import (
	"context"
	"decentralized-api/apiconfig"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cosmossdk.io/log"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/productscience/inference/app/beacon"
	"github.com/productscience/inference/app/beacon/sharefile"
	"github.com/productscience/inference/x/bls/types"
	"github.com/stretchr/testify/require"
	blst "github.com/supranational/blst/bindings/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	exportChainID = "gonka-test"
	exportAddr    = "gonka1me"
)

// exportFixture is a degree-t polynomial: slot s's secret is f(s+1), the
// group secret f(0); public keys are x·G2.
type exportFixture struct {
	secrets []fr.Element
	slotPKs [][]byte
	groupPK []byte
}

func g2(e fr.Element) []byte {
	be := e.Bytes()
	var le [32]byte
	for i := range be {
		le[i] = be[31-i]
	}
	return blst.P2Generator().Mult(le[:], 255).ToAffine().Compress()
}

func newExportFixture(t *testing.T, totalSlots, degree int) *exportFixture {
	t.Helper()
	coeffs := make([]fr.Element, degree+1)
	for i := range coeffs {
		_, err := coeffs[i].SetRandom()
		require.NoError(t, err)
	}
	f := &exportFixture{groupPK: g2(coeffs[0])}
	for s := 0; s < totalSlots; s++ {
		var x, acc fr.Element
		x.SetUint64(uint64(s) + 1)
		for i := len(coeffs) - 1; i >= 0; i-- {
			acc.Mul(&acc, &x)
			acc.Add(&acc, &coeffs[i])
		}
		f.secrets = append(f.secrets, acc)
		f.slotPKs = append(f.slotPKs, g2(acc))
	}
	return f
}

// result builds a final participant result for slots [start,end]. The shares
// are split across two consensus-valid dealers so ensureConsensusSharesComplete
// and the aggregation path see realistic input.
func (f *exportFixture) result(epochID uint64, start, end uint32) *VerificationResult {
	n := int(end-start) + 1
	d0 := make([]fr.Element, n)
	d1 := make([]fr.Element, n)
	agg := make([]fr.Element, n)
	for i := 0; i < n; i++ {
		_, _ = d0[i].SetRandom()
		d1[i].Sub(&f.secrets[int(start)+i], &d0[i])
		agg[i] = f.secrets[int(start)+i]
	}
	return &VerificationResult{
		EpochID:          epochID,
		DkgPhase:         types.DKGPhase_DKG_PHASE_COMPLETED,
		IsParticipant:    true,
		SlotRange:        [2]uint32{start, end},
		DealerShares:     [][]fr.Element{d0, d1},
		ValidDealers:     []bool{true, true},
		AggregatedShares: agg,
		GroupPublicKey:   f.groupPK,
	}
}

type fakeChain struct {
	chainErr     error
	current      atomic.Uint64
	currentErr   error
	recoverFn    func(epochID uint64) (*VerificationResult, error)
	recoverCalls atomic.Int64
	now          time.Time
}

func newTestManager(t *testing.T, chain *fakeChain) (*BlsManager, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bls-beacon")
	bm := NewBlsManager(createMockCosmosClient())
	if chain.now.IsZero() {
		chain.now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	}
	bm.configureBeaconExport(true, dir, beaconExportDeps{
		chainID: func(context.Context) (string, error) {
			if chain.chainErr != nil {
				return "", chain.chainErr
			}
			return exportChainID, nil
		},
		currentEpoch: func(context.Context) (uint64, error) {
			if chain.currentErr != nil {
				return 0, chain.currentErr
			}
			return chain.current.Load(), nil
		},
		participant: func() string { return exportAddr },
		recover: func(epochID uint64) (*VerificationResult, error) {
			chain.recoverCalls.Add(1)
			if chain.recoverFn == nil {
				return nil, status.Error(codes.NotFound, "no epoch")
			}
			return chain.recoverFn(epochID)
		},
		now: func() time.Time { return chain.now },
	})
	return bm, dir
}

func TestExportWritesValidatedFile(t *testing.T) {
	fx := newExportFixture(t, 16, 4)
	bm, dir := newTestManager(t, &fakeChain{})

	written, err := bm.exportBeaconShares(fx.result(5, 4, 9))
	require.NoError(t, err)
	require.True(t, written)

	path := filepath.Join(dir, "epoch-5.json")
	st, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	st, err = os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), st.Mode().Perm())

	f, err := sharefile.ReadFile(path, exportChainID)
	require.NoError(t, err)
	require.Equal(t, uint64(5), f.EpochID)
	require.Equal(t, exportAddr, f.ParticipantAddress)
	require.Equal(t, uint32(4), f.SlotStartIndex)
	require.Equal(t, uint32(9), f.SlotEndIndex)
	require.Equal(t, fx.groupPK, f.GroupPublicKeyBytes())
	for i, b := range f.SharesBigEndian() {
		require.Equal(t, fx.secrets[4+i].Bytes(), b)
	}

	// Same shares again: no rewrite.
	written, err = bm.exportBeaconShares(fx.result(5, 4, 9))
	require.NoError(t, err)
	require.False(t, written)
}

// TestExportToNodeProviderRoundTrip: what the dAPI writes is exactly what the
// node's FileShareProvider accepts, checked against committed slot public keys.
func TestExportToNodeProviderRoundTrip(t *testing.T) {
	fx := newExportFixture(t, 16, 4)
	bm, dir := newTestManager(t, &fakeChain{})
	_, err := bm.exportBeaconShares(fx.result(5, 4, 9))
	require.NoError(t, err)

	keys := beacon.CommittedKeysFunc(func(_ sdk.Context, epochID uint64, start, end uint32) (beacon.EpochKeys, error) {
		require.Equal(t, uint64(5), epochID)
		return beacon.EpochKeys{Ready: true, GroupPublicKey: fx.groupPK, SlotPublicKeys: fx.slotPKs[start : end+1], OwnerAddress: exportAddr}, nil
	})
	p, err := beacon.NewFileShareProvider(dir, keys, log.NewNopLogger())
	require.NoError(t, err)
	defer p.Close()

	shares, ok := p.Get(sdk.Context{}.WithChainID(exportChainID).WithBlockHeight(10), 5)
	require.True(t, ok)
	require.Len(t, shares.Scalars, 6)
	for i, sc := range shares.Scalars {
		require.NoError(t, beacon.VerifyShare(sc, fx.slotPKs[4+i]), "slot %d", 4+i)
	}
}

func TestExportSkipsNonFinalResults(t *testing.T) {
	fx := newExportFixture(t, 8, 2)
	cases := map[string]func(r *VerificationResult){
		"verifying phase":      func(r *VerificationResult) { r.DkgPhase = types.DKGPhase_DKG_PHASE_VERIFYING },
		"not a participant":    func(r *VerificationResult) { r.IsParticipant = false },
		"no group key":         func(r *VerificationResult) { r.GroupPublicKey = nil },
		"short group key":      func(r *VerificationResult) { r.GroupPublicKey = r.GroupPublicKey[:48] },
		"nil-safe (no result)": nil,
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bm, dir := newTestManager(t, &fakeChain{})
			var r *VerificationResult
			if mutate != nil {
				r = fx.result(3, 0, 3)
				mutate(r)
			}
			written, err := bm.exportBeaconShares(r)
			require.NoError(t, err)
			require.False(t, written)
			_, err = os.Stat(filepath.Join(dir, "epoch-3.json"))
			require.True(t, os.IsNotExist(err))
		})
	}
	// SIGNED is final.
	bm, _ := newTestManager(t, &fakeChain{})
	r := fx.result(3, 0, 3)
	r.DkgPhase = types.DKGPhase_DKG_PHASE_SIGNED
	written, err := bm.exportBeaconShares(r)
	require.NoError(t, err)
	require.True(t, written)
}

func TestExportRejectsIncompleteShares(t *testing.T) {
	fx := newExportFixture(t, 8, 2)
	bm, dir := newTestManager(t, &fakeChain{})

	r := fx.result(3, 0, 3)
	r.DealerShares[1] = r.DealerShares[1][:2] // a valid dealer is missing slots
	_, err := bm.exportBeaconShares(r)
	require.ErrorContains(t, err, "incomplete")

	r = fx.result(3, 0, 3)
	r.AggregatedShares = r.AggregatedShares[:3]
	r.ValidDealers = nil // skip the dealer check, keep the length check
	_, err = bm.exportBeaconShares(r)
	require.ErrorContains(t, err, "3 aggregated shares for 4 slots")

	_, err = os.Stat(filepath.Join(dir, "epoch-3.json"))
	require.True(t, os.IsNotExist(err))
}

func TestExportDisabledAndChainIDErrors(t *testing.T) {
	fx := newExportFixture(t, 8, 2)

	// Default manager (export never configured): no-op.
	bm := NewBlsManager(createMockCosmosClient())
	written, err := bm.exportBeaconShares(fx.result(3, 0, 3))
	require.NoError(t, err)
	require.False(t, written)

	// Disabled by config.
	dir := filepath.Join(t.TempDir(), "bls-beacon")
	bm.configureBeaconExport(false, dir, beaconExportDeps{})
	written, err = bm.exportBeaconShares(fx.result(3, 0, 3))
	require.NoError(t, err)
	require.False(t, written)
	_, err = os.Stat(dir)
	require.True(t, os.IsNotExist(err))

	// Chain id unavailable: nothing written, error reported.
	bm2, dir2 := newTestManager(t, &fakeChain{chainErr: errors.New("node down")})
	_, err = bm2.exportBeaconShares(fx.result(3, 0, 3))
	require.ErrorContains(t, err, "node down")
	_, err = os.Stat(filepath.Join(dir2, "epoch-3.json"))
	require.True(t, os.IsNotExist(err))
}

func TestConfigureBeaconShareExportFromConfig(t *testing.T) {
	bm := NewBlsManager(createMockCosmosClient())
	bm.ConfigureBeaconShareExport(apiconfig.BlsBeaconConfig{})
	enabled, dir := bm.beaconExportEnabled()
	require.True(t, enabled, "zero config = export on")
	require.Equal(t, apiconfig.DefaultBlsBeaconShareDir, dir)

	bm.ConfigureBeaconShareExport(apiconfig.BlsBeaconConfig{ExportDisabled: true, ShareDir: "/x"})
	enabled, dir = bm.beaconExportEnabled()
	require.False(t, enabled)
	require.Equal(t, "/x", dir)
}

func TestStoreVerificationResultExports(t *testing.T) {
	fx := newExportFixture(t, 8, 2)
	bm, dir := newTestManager(t, &fakeChain{})

	// A VERIFYING result is cached but not exported...
	r := fx.result(4, 0, 3)
	r.DkgPhase = types.DKGPhase_DKG_PHASE_VERIFYING
	bm.storeVerificationResult(r)
	_, err := os.Stat(filepath.Join(dir, "epoch-4.json"))
	require.True(t, os.IsNotExist(err))

	// ...the COMPLETED one is.
	bm.storeVerificationResult(fx.result(4, 0, 3))
	_, err = sharefile.ReadFile(filepath.Join(dir, "epoch-4.json"), exportChainID)
	require.NoError(t, err)

	// Export failure never panics or blocks storing.
	bad := fx.result(5, 0, 3)
	bad.DealerShares[0] = nil
	require.NotPanics(t, func() { bm.storeVerificationResult(bad) })
	require.NotNil(t, bm.GetVerificationResult(5))
}

func TestExportPrunesOldEpochs(t *testing.T) {
	fx := newExportFixture(t, 8, 2)
	bm, dir := newTestManager(t, &fakeChain{})
	for _, e := range []uint64{2, 3, 4, 5} {
		_, err := bm.exportBeaconShares(fx.result(e, 0, 3))
		require.NoError(t, err)
	}
	var names []string
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	// Exporting epoch 5 keeps 3 and up (current-1 when 5 is the next epoch).
	require.ElementsMatch(t, []string{"epoch-3.json", "epoch-4.json", "epoch-5.json"}, names)
}

func TestReconcileReexportsMissingFileFromCache(t *testing.T) {
	fx := newExportFixture(t, 8, 2)
	chain := &fakeChain{}
	chain.current.Store(4)
	bm, dir := newTestManager(t, chain)
	bm.cache.Store(fx.result(4, 0, 3)) // cached without going through the export hook
	path := filepath.Join(dir, "epoch-4.json")

	bm.reconcileBeaconShares(context.Background())
	_, err := sharefile.ReadFile(path, exportChainID)
	require.NoError(t, err)

	// Deleted by someone: the next tick writes it again (no backoff after success).
	require.NoError(t, os.Remove(path))
	bm.reconcileBeaconShares(context.Background())
	_, err = sharefile.ReadFile(path, exportChainID)
	require.NoError(t, err)

	// Corrupted: replaced with a valid file.
	require.NoError(t, os.WriteFile(path, []byte("{garbage"), 0o600))
	chain.now = chain.now.Add(beaconRecoverBackoff)
	bm.reconcileBeaconShares(context.Background())
	_, err = sharefile.ReadFile(path, exportChainID)
	require.NoError(t, err)
}

// TestReconcileRecoversAfterRestart: an empty cache (fresh dAPI process) and no
// file. Recovery from chain stores the result, which exports it.
func TestReconcileRecoversAfterRestart(t *testing.T) {
	fx := newExportFixture(t, 8, 2)
	chain := &fakeChain{}
	chain.current.Store(6)
	bm, dir := newTestManager(t, chain)
	chain.recoverFn = func(epochID uint64) (*VerificationResult, error) {
		if epochID != 6 {
			return nil, status.Error(codes.NotFound, "no epoch")
		}
		r := fx.result(6, 0, 3)
		bm.storeVerificationResult(r) // what setupAndPerformVerification does
		return r, nil
	}

	bm.reconcileBeaconShares(context.Background())
	_, err := sharefile.ReadFile(filepath.Join(dir, "epoch-6.json"), exportChainID)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "epoch-7.json"))
	require.True(t, os.IsNotExist(err), "next epoch has no DKG yet")
	require.Equal(t, int64(2), chain.recoverCalls.Load(), "epochs 6 and 7")

	// Epoch 7 is not retried within the backoff; 6 is done.
	bm.reconcileBeaconShares(context.Background())
	require.Equal(t, int64(2), chain.recoverCalls.Load())

	// After the backoff, epoch 7 is retried.
	chain.now = chain.now.Add(beaconRecoverBackoff)
	bm.reconcileBeaconShares(context.Background())
	require.Equal(t, int64(3), chain.recoverCalls.Load())
}

func TestReconcileNonParticipantAndErrors(t *testing.T) {
	chain := &fakeChain{}
	chain.current.Store(6)
	chain.recoverFn = func(epochID uint64) (*VerificationResult, error) {
		return &VerificationResult{EpochID: epochID, IsParticipant: false}, nil
	}
	bm, dir := newTestManager(t, chain)
	bm.reconcileBeaconShares(context.Background())
	entries, _ := os.ReadDir(dir)
	require.Empty(t, entries)

	// Current epoch unavailable: nothing happens, no panic.
	chain.currentErr = errors.New("rpc down")
	calls := chain.recoverCalls.Load()
	require.NotPanics(t, func() { bm.reconcileBeaconShares(context.Background()) })
	require.Equal(t, calls, chain.recoverCalls.Load())

	// A panicking dependency is contained.
	chain.currentErr = nil
	chain.now = chain.now.Add(beaconRecoverBackoff)
	chain.recoverFn = func(uint64) (*VerificationResult, error) { panic("boom") }
	require.NotPanics(t, func() { bm.reconcileBeaconShares(context.Background()) })
}

func TestReconcilePrunesBelowPreviousEpoch(t *testing.T) {
	fx := newExportFixture(t, 8, 2)
	chain := &fakeChain{}
	chain.current.Store(3)
	bm, dir := newTestManager(t, chain)
	for _, e := range []uint64{1, 2, 3} {
		bm.cache.Store(fx.result(e, 0, 3))
		_, err := bm.exportBeaconShares(fx.result(e, 0, 3))
		require.NoError(t, err)
	}
	chain.current.Store(5)
	bm.cache.Store(fx.result(5, 0, 3))
	bm.reconcileBeaconShares(context.Background())
	var names []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	require.ElementsMatch(t, []string{"epoch-5.json"}, names, "epochs below current-1 are removed")
}

func TestStartBeaconShareExportLoop(t *testing.T) {
	fx := newExportFixture(t, 8, 2)
	chain := &fakeChain{}
	chain.current.Store(2)
	bm, dir := newTestManager(t, chain)
	bm.cache.Store(fx.result(2, 0, 3))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bm.StartBeaconShareExport(ctx, 20*time.Millisecond)
	path := filepath.Join(dir, "epoch-2.json")
	require.Eventually(t, func() bool {
		_, err := sharefile.ReadFile(path, exportChainID)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)

	require.NoError(t, os.Remove(path))
	require.Eventually(t, func() bool {
		_, err := sharefile.ReadFile(path, exportChainID)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "deleted file is re-exported by the loop")
}
