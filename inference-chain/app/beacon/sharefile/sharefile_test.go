package sharefile

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/stretchr/testify/require"
)

const testChainID = "gonka-test"

var fixedTime = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// fixtureShares returns deterministic non-zero fr shares.
func fixtureShares(n int, seed uint64) [][ScalarLen]byte {
	out := make([][ScalarLen]byte, n)
	for i := range out {
		var e fr.Element
		e.SetUint64(seed*1000 + uint64(i) + 1)
		e.Square(&e) // spread the bits
		out[i] = e.Bytes()
	}
	return out
}

func fixtureGroupKey() []byte {
	b := make([]byte, GroupPublicKeyLen)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func newFixture(t *testing.T) *File {
	t.Helper()
	f, err := New(testChainID, 7, "gonka1participant", 40, 47, fixtureGroupKey(), fixtureShares(8, 7), fixedTime)
	require.NoError(t, err)
	return f
}

func TestFileNames(t *testing.T) {
	require.Equal(t, "epoch-12.json", FileName(12))
	id, ok := ParseFileName("epoch-12.json")
	require.True(t, ok)
	require.Equal(t, uint64(12), id)
	for _, bad := range []string{"epoch-.json", "epoch-1.json.tmp", ".epoch-1.tmp", "epoch-x.json", "epoch-99999999999999999999.json", "xepoch-1.json"} {
		_, ok := ParseFileName(bad)
		require.False(t, ok, bad)
	}
}

func TestRoundTrip(t *testing.T) {
	f := newFixture(t)
	data, err := f.Marshal()
	require.NoError(t, err)

	got, err := Unmarshal(data, testChainID)
	require.NoError(t, err)
	require.Equal(t, f, got)
	require.Equal(t, fixtureShares(8, 7), got.SharesBigEndian())
	require.Equal(t, fixtureGroupKey(), got.GroupPublicKeyBytes())
	require.Equal(t, 8, got.NumSlots())
}

// TestChecksumIgnoresCreatedAt: re-exporting the same shares later is a no-op.
func TestChecksumIgnoresCreatedAt(t *testing.T) {
	a := newFixture(t)
	b, err := New(testChainID, 7, "gonka1participant", 40, 47, fixtureGroupKey(), fixtureShares(8, 7), fixedTime.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, a.SHA256, b.SHA256)
	require.NotEqual(t, a.CreatedAt, b.CreatedAt)
}

// TestCrossFixture pins the v1 wire format: the dAPI exporter and the node
// provider both parse testdata/epoch-share-v1.json (plan Step 7 item 11).
func TestCrossFixture(t *testing.T) {
	path := filepath.Join("testdata", "epoch-share-v1.json")
	f := newFixture(t)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		data, err := f.Marshal()
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0o644))
	}
	golden, err := os.ReadFile(path)
	require.NoError(t, err)
	got, err := Unmarshal(golden, testChainID)
	require.NoError(t, err)
	require.Equal(t, f, got)
	data, err := f.Marshal()
	require.NoError(t, err)
	require.Equal(t, string(golden), string(data), "format changed: bump Version or rerun with UPDATE_GOLDEN=1")
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *File)
		chain  string
		want   string
	}{
		{"wrong chain", func(f *File) {}, "other-chain", "chain_id mismatch"},
		{"version", func(f *File) { f.Version = 2 }, "", "unsupported version"},
		{"empty chain", func(f *File) { f.ChainID = "" }, "", "empty chain_id"},
		{"empty participant", func(f *File) { f.ParticipantAddress = "" }, "", "empty participant_address"},
		{"inverted range", func(f *File) { f.SlotStartIndex, f.SlotEndIndex = 47, 40 }, "", "invalid slot range"},
		{"too few shares", func(f *File) { f.Shares = f.Shares[:7] }, "", "7 shares for 8 slots"},
		{"short group key", func(f *File) { f.GroupPublicKey = f.GroupPublicKey[:10] }, "", "group_public_key"},
		{"bad hex share", func(f *File) { f.Shares[0] = "zz" + f.Shares[0][2:] }, "", "share 0"},
		{"short share", func(f *File) { f.Shares[1] = f.Shares[1][:62] }, "", "share 1"},
		{"zero share", func(f *File) { f.Shares[2] = strings.Repeat("00", ScalarLen) }, "", "share 2 is not"},
		{"share >= modulus", func(f *File) { f.Shares[3] = hex.EncodeToString(fr.Modulus().FillBytes(make([]byte, 32))) }, "", "share 3 is not"},
		{"checksum", func(f *File) { f.EpochID = 8 }, "", "checksum"},
		{"slot range too large", func(f *File) { f.SlotStartIndex, f.SlotEndIndex = 0, MaxSlots }, "", "too large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.mutate(f)
			err := f.Validate(tc.chain)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
	f := newFixture(t)
	require.True(t, errors.Is(f.Validate("other"), ErrChainID))
	f.SHA256 = strings.Repeat("0", 64)
	require.True(t, errors.Is(f.Validate(""), ErrChecksum))
}

func TestUnmarshalRejectsMalformed(t *testing.T) {
	f := newFixture(t)
	data, err := f.Marshal()
	require.NoError(t, err)

	_, err = Unmarshal(data[:len(data)/2], "")
	require.Error(t, err, "truncated")

	withExtra := strings.Replace(string(data), `"version": 1,`, `"version": 1, "extra": 1,`, 1)
	_, err = Unmarshal([]byte(withExtra), "")
	require.ErrorContains(t, err, "unknown field")

	_, err = Unmarshal(append(append([]byte{}, data...), []byte("{}")...), "")
	require.ErrorContains(t, err, "trailing")

	_, err = Unmarshal([]byte(""), "")
	require.Error(t, err)
}

func TestWriteAtomicPermissionsAndIdempotence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bls-beacon")
	f := newFixture(t)

	path, written, err := WriteAtomic(dir, f)
	require.NoError(t, err)
	require.True(t, written)
	require.Equal(t, filepath.Join(dir, "epoch-7.json"), path)

	st, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, DirMode, st.Mode().Perm())
	st, err = os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, FileMode, st.Mode().Perm())

	got, err := ReadFile(path, testChainID)
	require.NoError(t, err)
	require.Equal(t, f.SHA256, got.SHA256)

	// Same shares, later timestamp: no rewrite.
	again, err := New(testChainID, 7, "gonka1participant", 40, 47, fixtureGroupKey(), fixtureShares(8, 7), fixedTime.Add(time.Hour))
	require.NoError(t, err)
	_, written, err = WriteAtomic(dir, again)
	require.NoError(t, err)
	require.False(t, written)

	// A loosened mode is tightened back even when content is unchanged.
	require.NoError(t, os.Chmod(path, 0o644))
	_, _, err = WriteAtomic(dir, again)
	require.NoError(t, err)
	st, _ = os.Stat(path)
	require.Equal(t, FileMode, st.Mode().Perm())

	// Different shares: rewritten.
	changed, err := New(testChainID, 7, "gonka1participant", 40, 47, fixtureGroupKey(), fixtureShares(8, 8), fixedTime)
	require.NoError(t, err)
	_, written, err = WriteAtomic(dir, changed)
	require.NoError(t, err)
	require.True(t, written)

	// An existing loose directory is tightened.
	require.NoError(t, os.Chmod(dir, 0o755))
	_, _, err = WriteAtomic(dir, changed)
	require.NoError(t, err)
	st, _ = os.Stat(dir)
	require.Equal(t, DirMode, st.Mode().Perm())

	// No temp files left behind.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestWriteAtomicRejectsInvalid(t *testing.T) {
	f := newFixture(t)
	f.Shares = f.Shares[:1]
	_, _, err := WriteAtomic(t.TempDir(), f)
	require.Error(t, err)
}

// TestConcurrentReaderNeverSeesPartialFile rewrites the file in a loop while
// readers parse it; every successful open must parse and validate.
func TestConcurrentReaderNeverSeesPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName(7))
	versions := make([]*File, 2)
	for i := range versions {
		f, err := New(testChainID, 7, "gonka1participant", 0, 255, fixtureGroupKey(), fixtureShares(256, uint64(i+1)), fixedTime)
		require.NoError(t, err)
		versions[i] = f
	}
	_, _, err := WriteAtomic(dir, versions[0])
	require.NoError(t, err)

	var stop atomic.Bool
	var reads, failures atomic.Int64
	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				f, err := ReadFile(path, testChainID)
				reads.Add(1)
				if err != nil || (f.SHA256 != versions[0].SHA256 && f.SHA256 != versions[1].SHA256) {
					failures.Add(1)
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		_, _, err := WriteAtomic(dir, versions[i%2])
		require.NoError(t, err)
	}
	stop.Store(true)
	wg.Wait()
	require.Positive(t, reads.Load())
	require.Zero(t, failures.Load(), "a reader observed a partial or invalid file")
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	for _, e := range []uint64{3, 4, 5, 6} {
		f, err := New(testChainID, e, "gonka1participant", 0, 1, fixtureGroupKey(), fixtureShares(2, e), fixedTime)
		require.NoError(t, err)
		_, _, err = WriteAtomic(dir, f)
		require.NoError(t, err)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "unrelated.json"), []byte("{}"), 0o600))
	staleTmp := filepath.Join(dir, ".epoch-123.tmp")
	require.NoError(t, os.WriteFile(staleTmp, []byte("x"), 0o600))
	old := time.Now().Add(-2 * time.Minute)
	require.NoError(t, os.Chtimes(staleTmp, old, old))
	freshTmp := filepath.Join(dir, ".epoch-456.tmp")
	require.NoError(t, os.WriteFile(freshTmp, []byte("x"), 0o600))

	removed, err := Prune(dir, 5)
	require.NoError(t, err)
	require.ElementsMatch(t, []uint64{3, 4}, removed)

	var names []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	require.ElementsMatch(t, []string{"epoch-5.json", "epoch-6.json", "unrelated.json", ".epoch-456.tmp"}, names)

	removed, err = Prune(filepath.Join(dir, "missing"), 5)
	require.NoError(t, err)
	require.Empty(t, removed)
}

func TestReadFileTooLarge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "epoch-1.json")
	require.NoError(t, os.WriteFile(path, make([]byte, MaxFileBytes+1), 0o600))
	_, err := ReadFile(path, "")
	require.ErrorContains(t, err, "too large")
}
