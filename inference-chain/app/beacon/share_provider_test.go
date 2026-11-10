package beacon

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cosmossdk.io/log"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/stretchr/testify/require"
	blst "github.com/supranational/blst/bindings/go"

	"github.com/productscience/inference/app/beacon/sharefile"
)

const (
	testChain = "gonka-test"
	testAddr  = "gonka1owner"
)

// dkgFixture is a degree-t polynomial f over fr: slot i's secret is f(i+1),
// the group secret is f(0). Public keys are x·G2 (compressed).
type dkgFixture struct {
	totalSlots uint32
	secrets    []fr.Element // per slot
	slotPKs    [][]byte
	groupPK    []byte
}

func newDKGFixture(t *testing.T, totalSlots uint32, degree int) *dkgFixture {
	t.Helper()
	coeffs := make([]fr.Element, degree+1)
	for i := range coeffs {
		_, err := coeffs[i].SetRandom()
		require.NoError(t, err)
	}
	eval := func(x uint64) fr.Element {
		var xe, acc fr.Element
		xe.SetUint64(x)
		for i := len(coeffs) - 1; i >= 0; i-- {
			acc.Mul(&acc, &xe)
			acc.Add(&acc, &coeffs[i])
		}
		return acc
	}
	f := &dkgFixture{totalSlots: totalSlots}
	for s := uint32(0); s < totalSlots; s++ {
		v := eval(uint64(s) + 1)
		f.secrets = append(f.secrets, v)
		f.slotPKs = append(f.slotPKs, g2Mul(v))
	}
	f.groupPK = g2Mul(coeffs[0])
	return f
}

func leBytes(e fr.Element) [32]byte {
	be := e.Bytes()
	var le [32]byte
	for i := range be {
		le[i] = be[31-i]
	}
	return le
}

func g2Mul(e fr.Element) []byte {
	le := leBytes(e)
	return blst.P2Generator().Mult(le[:], 255).ToAffine().Compress()
}

func (f *dkgFixture) sharesBE(start, end uint32) [][32]byte {
	out := make([][32]byte, 0, end-start+1)
	for s := start; s <= end; s++ {
		out = append(out, f.secrets[s].Bytes())
	}
	return out
}

func (f *dkgFixture) writeFile(t *testing.T, dir, chainID string, epoch uint64, start, end uint32, shares [][32]byte, groupPK []byte) string {
	t.Helper()
	if shares == nil {
		shares = f.sharesBE(start, end)
	}
	if groupPK == nil {
		groupPK = f.groupPK
	}
	file, err := sharefile.New(chainID, epoch, testAddr, start, end, groupPK, shares, time.Now())
	require.NoError(t, err)
	path, _, err := sharefile.WriteAtomic(dir, file)
	require.NoError(t, err)
	return path
}

// fakeKeys serves committed keys from fixtures per epoch and counts calls.
type fakeKeys struct {
	mu       sync.Mutex
	fixtures map[uint64]*dkgFixture
	owners   map[uint64]string // epoch -> owner of every range (simplification)
	ready    bool
	err      error
	panicMsg string
	calls    atomic.Int64
}

func (k *fakeKeys) EpochKeys(_ sdk.Context, epochID uint64, start, end uint32) (EpochKeys, error) {
	k.calls.Add(1)
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.panicMsg != "" {
		panic(k.panicMsg)
	}
	if k.err != nil {
		return EpochKeys{}, k.err
	}
	if !k.ready {
		return EpochKeys{Ready: false}, nil
	}
	f, ok := k.fixtures[epochID]
	if !ok {
		return EpochKeys{}, errors.New("no such epoch")
	}
	owner := testAddr
	if o, ok := k.owners[epochID]; ok {
		owner = o
	}
	return EpochKeys{Ready: true, GroupPublicKey: f.groupPK, SlotPublicKeys: f.slotPKs[start : end+1], OwnerAddress: owner}, nil
}

func newKeys(fixtures map[uint64]*dkgFixture) *fakeKeys {
	return &fakeKeys{fixtures: fixtures, owners: map[uint64]string{}, ready: true}
}

func ctxAt(chainID string, height int64) sdk.Context {
	return sdk.Context{}.WithChainID(chainID).WithBlockHeight(height)
}

func newProvider(t *testing.T, dir string, keys CommittedKeys) *FileShareProvider {
	t.Helper()
	p, err := NewFileShareProvider(dir, keys, log.NewNopLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestProviderRoundTripValidShares(t *testing.T) {
	dir := t.TempDir()
	fx := newDKGFixture(t, 16, 4)
	fx.writeFile(t, dir, testChain, 5, 4, 9, nil, nil)
	keys := newKeys(map[uint64]*dkgFixture{5: fx})
	p := newProvider(t, dir, keys)

	got, ok := p.Get(ctxAt(testChain, 100), 5)
	require.True(t, ok)
	require.Equal(t, uint64(5), got.EpochID)
	require.Equal(t, uint32(4), got.SlotStart)
	require.Equal(t, uint32(9), got.SlotEnd)
	require.Equal(t, testAddr, got.ParticipantAddress)
	require.Equal(t, fx.groupPK, got.GroupPublicKey)
	require.Len(t, got.Scalars, 6)
	for i := range got.Scalars {
		require.Equal(t, leBytes(fx.secrets[4+i]), got.Scalars[i], "slot %d", 4+i)
		require.NoError(t, VerifyShare(got.Scalars[i], fx.slotPKs[4+i]))
	}

	// Verified once; later calls are served from memory.
	for h := int64(101); h < 110; h++ {
		_, ok := p.Get(ctxAt(testChain, h), 5)
		require.True(t, ok)
	}
	require.Equal(t, int64(1), keys.calls.Load())
	require.Equal(t, []Status{{EpochID: 5, SlotStart: 4, SlotEnd: 9, State: "valid"}}, p.Statuses())

	// Another epoch is not served.
	_, ok = p.Get(ctxAt(testChain, 110), 6)
	require.False(t, ok)
}

func TestProviderRejectsAgainstCommittedState(t *testing.T) {
	fx := newDKGFixture(t, 16, 4)
	other := newDKGFixture(t, 16, 4)

	cases := []struct {
		name  string
		write func(t *testing.T, dir string)
		keys  func(k *fakeKeys)
		// calls is how often committed state is read: once, or never when
		// the file is rejected before that (chain id).
		calls int64
	}{
		{
			name:  "group public key mismatch",
			write: func(t *testing.T, dir string) { fx.writeFile(t, dir, testChain, 5, 4, 9, nil, other.groupPK) },
		},
		{
			name: "share does not match slot public key",
			write: func(t *testing.T, dir string) {
				shares := fx.sharesBE(4, 9)
				shares[3] = other.secrets[7].Bytes()
				fx.writeFile(t, dir, testChain, 5, 4, 9, shares, nil)
			},
		},
		{
			name:  "shares from another DKG",
			write: func(t *testing.T, dir string) { fx.writeFile(t, dir, testChain, 5, 4, 9, other.sharesBE(4, 9), nil) },
		},
		{
			name:  "slot range owned by someone else",
			write: func(t *testing.T, dir string) { fx.writeFile(t, dir, testChain, 5, 4, 9, nil, nil) },
			keys:  func(k *fakeKeys) { k.owners[5] = "gonka1someoneelse" },
		},
		{
			name:  "wrong chain",
			write: func(t *testing.T, dir string) { fx.writeFile(t, dir, "other-chain", 5, 4, 9, nil, nil) },
			calls: -1,
		},
		{
			name:  "committed keys error",
			write: func(t *testing.T, dir string) { fx.writeFile(t, dir, testChain, 5, 4, 9, nil, nil) },
			keys:  func(k *fakeKeys) { k.err = errors.New("epoch 5 DKG failed") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.write(t, dir)
			keys := newKeys(map[uint64]*dkgFixture{5: fx})
			if tc.keys != nil {
				tc.keys(keys)
			}
			p := newProvider(t, dir, keys)
			for h := int64(100); h < 105; h++ {
				_, ok := p.Get(ctxAt(testChain, h), 5)
				require.False(t, ok)
			}
			wantCalls := int64(1)
			if tc.calls < 0 {
				wantCalls = 0
			}
			require.Equal(t, wantCalls, keys.calls.Load(), "a rejected file is not re-verified until it changes")
			st := p.Statuses()
			require.Len(t, st, 1)
			require.Equal(t, "invalid", st[0].State)
			require.NotEmpty(t, st[0].Reason)
		})
	}
}

func TestProviderRecoversWhenFileIsFixed(t *testing.T) {
	dir := t.TempDir()
	fx := newDKGFixture(t, 8, 2)
	other := newDKGFixture(t, 8, 2)
	fx.writeFile(t, dir, testChain, 3, 0, 3, other.sharesBE(0, 3), nil)
	p := newProvider(t, dir, newKeys(map[uint64]*dkgFixture{3: fx}))

	_, ok := p.Get(ctxAt(testChain, 10), 3)
	require.False(t, ok)

	fx.writeFile(t, dir, testChain, 3, 0, 3, nil, nil)
	p.Reload()
	_, ok = p.Get(ctxAt(testChain, 11), 3)
	require.True(t, ok)
}

func TestProviderNotReadyRetriesLater(t *testing.T) {
	dir := t.TempDir()
	fx := newDKGFixture(t, 8, 2)
	fx.writeFile(t, dir, testChain, 3, 0, 3, nil, nil)
	keys := newKeys(map[uint64]*dkgFixture{3: fx})
	keys.ready = false
	p := newProvider(t, dir, keys)

	_, ok := p.Get(ctxAt(testChain, 100), 3)
	require.False(t, ok)
	for h := int64(101); h < 100+verifyRetryBlocks; h++ {
		_, ok := p.Get(ctxAt(testChain, h), 3)
		require.False(t, ok)
	}
	require.Equal(t, int64(1), keys.calls.Load(), "no re-check before the retry height")

	keys.mu.Lock()
	keys.ready = true
	keys.mu.Unlock()
	_, ok = p.Get(ctxAt(testChain, 100+verifyRetryBlocks), 3)
	require.True(t, ok)
	require.Equal(t, int64(2), keys.calls.Load())
}

func TestProviderChainMismatchAfterVerification(t *testing.T) {
	dir := t.TempDir()
	fx := newDKGFixture(t, 8, 2)
	fx.writeFile(t, dir, testChain, 3, 0, 3, nil, nil)
	p := newProvider(t, dir, newKeys(map[uint64]*dkgFixture{3: fx}))
	_, ok := p.Get(ctxAt(testChain, 10), 3)
	require.True(t, ok)
	_, ok = p.Get(ctxAt("other-chain", 11), 3)
	require.False(t, ok)
}

func TestProviderRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	fx := newDKGFixture(t, 8, 2)

	// Loose permissions.
	path := fx.writeFile(t, dir, testChain, 3, 0, 3, nil, nil)
	require.NoError(t, os.Chmod(path, 0o644))
	// Truncated file.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "epoch-4.json"), data[:len(data)/2], 0o600))
	// File name and content disagree.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "epoch-5.json"), data, 0o600))

	p := newProvider(t, dir, newKeys(map[uint64]*dkgFixture{3: fx, 4: fx, 5: fx}))
	require.Empty(t, p.Statuses(), "none of the three files is loaded")
	// Only epoch 3 is asked for: asking for 5 would advance the floor past 3.
	_, ok := p.Get(ctxAt(testChain, 10), 3)
	require.False(t, ok)

	// Fixing the mode makes epoch 3 load on the next reload.
	require.NoError(t, os.Chmod(path, 0o600))
	p.Reload()
	_, ok = p.Get(ctxAt(testChain, 11), 3)
	require.True(t, ok)
}

func TestProviderNeverPanics(t *testing.T) {
	dir := t.TempDir()
	fx := newDKGFixture(t, 8, 2)
	fx.writeFile(t, dir, testChain, 3, 0, 3, nil, nil)
	keys := newKeys(map[uint64]*dkgFixture{3: fx})
	keys.panicMsg = "keeper exploded"
	p := newProvider(t, dir, keys)
	require.NotPanics(t, func() {
		_, ok := p.Get(ctxAt(testChain, 10), 3)
		require.False(t, ok)
	})
}

func TestProviderKeepsOnlyRecentEpochs(t *testing.T) {
	dir := t.TempDir()
	fixtures := map[uint64]*dkgFixture{}
	for _, e := range []uint64{7, 8, 9, 10} {
		fixtures[e] = newDKGFixture(t, 8, 2)
		fixtures[e].writeFile(t, dir, testChain, e, 0, 3, nil, nil)
	}
	p := newProvider(t, dir, newKeys(fixtures))
	require.Len(t, p.Statuses(), 4)

	_, ok := p.Get(ctxAt(testChain, 10), 9)
	require.True(t, ok)
	var epochs []uint64
	for _, s := range p.Statuses() {
		epochs = append(epochs, s.EpochID)
	}
	require.ElementsMatch(t, []uint64{8, 9, 10}, epochs)

	// A reload does not bring the dropped epoch back.
	p.Reload()
	require.Len(t, p.Statuses(), 3)
	_, ok = p.Get(ctxAt(testChain, 11), 7)
	require.False(t, ok)
}

func TestProviderWatchesDirectory(t *testing.T) {
	dir := t.TempDir()
	fx := newDKGFixture(t, 8, 2)
	p := newProvider(t, dir, newKeys(map[uint64]*dkgFixture{3: fx}))
	p.SetPollInterval(50 * time.Millisecond)
	p.Start(t.Context())

	_, ok := p.Get(ctxAt(testChain, 10), 3)
	require.False(t, ok)

	path := fx.writeFile(t, dir, testChain, 3, 0, 3, nil, nil)
	require.Eventually(t, func() bool {
		_, ok := p.Get(ctxAt(testChain, 11), 3)
		return ok
	}, 5*time.Second, 20*time.Millisecond)

	require.NoError(t, os.Remove(path))
	require.Eventually(t, func() bool {
		_, ok := p.Get(ctxAt(testChain, 12), 3)
		return !ok
	}, 5*time.Second, 20*time.Millisecond)

	// Re-export after deletion (dAPI recovery) is picked up again.
	fx.writeFile(t, dir, testChain, 3, 0, 3, nil, nil)
	require.Eventually(t, func() bool {
		_, ok := p.Get(ctxAt(testChain, 13), 3)
		return ok
	}, 5*time.Second, 20*time.Millisecond)
}

func TestProviderConcurrentGetAndReload(t *testing.T) {
	dir := t.TempDir()
	fx := newDKGFixture(t, 32, 8)
	fx.writeFile(t, dir, testChain, 3, 0, 15, nil, nil)
	p := newProvider(t, dir, newKeys(map[uint64]*dkgFixture{3: fx}))

	var stop atomic.Bool
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for h := int64(1); !stop.Load(); h++ {
				if s, ok := p.Get(ctxAt(testChain, h), 3); ok {
					require.Len(t, s.Scalars, 16)
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		fx.writeFile(t, dir, testChain, 3, 0, 15, nil, nil)
		p.Reload()
	}
	stop.Store(true)
	wg.Wait()
	_, ok := p.Get(ctxAt(testChain, 1_000_000), 3)
	require.True(t, ok)
}

func TestVerifyShareRejectsGarbageKey(t *testing.T) {
	var s [32]byte
	_, _ = rand.Read(s[:16])
	require.Error(t, VerifyShare(s, []byte{1, 2, 3}))
}

func TestNewFileShareProviderArgs(t *testing.T) {
	_, err := NewFileShareProvider("", newKeys(nil), nil)
	require.Error(t, err)
	_, err = NewFileShareProvider(t.TempDir(), nil, nil)
	require.Error(t, err)

	dir := filepath.Join(t.TempDir(), "nested", "bls-beacon")
	p, err := NewFileShareProvider(dir, newKeys(nil), nil)
	require.NoError(t, err)
	require.Equal(t, dir, p.Dir())
	st, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, sharefile.DirMode, st.Mode().Perm())
	require.NoError(t, p.Close(), "Close without Start is a no-op")
}
