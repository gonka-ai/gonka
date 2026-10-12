package spool

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpen_DisabledEmptyPath(t *testing.T) {
	d, err := Open(Config{AllowUnlimited: true})
	require.NoError(t, err)
	require.False(t, d.Enabled())
	_, err = d.Create()
	require.ErrorIs(t, err, ErrDisabled)
}

func TestOpen_RejectsUnlimitedWithoutFlag(t *testing.T) {
	_, err := Open(Config{Path: t.TempDir(), Prefix: "t-"})
	require.ErrorIs(t, err, ErrUnlimitedRejected)
}

func TestOpen_PrefixSweepLeavesSibling(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agg-old.sse"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "keep-me"), []byte("y"), 0o600))

	d, err := Open(Config{
		Path:         dir,
		Prefix:       "agg-",
		MaxFiles:     4,
		MaxFileBytes: 1 << 20,
	})
	require.NoError(t, err)
	require.True(t, d.Enabled())
	require.Equal(t, 1, d.Stats().SweepCount)

	_, err = os.Stat(filepath.Join(dir, "agg-old.sse"))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(dir, "keep-me"))
	require.NoError(t, err)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestCreate_UnlinkByDefault(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(Config{Path: dir, Prefix: "agg-", MaxFiles: 2, MaxFileBytes: 1 << 20})
	require.NoError(t, err)
	f, err := d.Create()
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.Equal(t, int64(1), d.Stats().FilesOpen)

	_, err = f.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.Equal(t, int64(0), d.Stats().FilesOpen)
}

func TestCreate_KeepNamed(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(Config{Path: dir, Prefix: "agg-", KeepNamed: true, MaxFiles: 2, MaxFileBytes: 1 << 20})
	require.NoError(t, err)
	f, err := d.Create()
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestCreate_MaxFiles(t *testing.T) {
	d, err := Open(Config{Path: t.TempDir(), Prefix: "x-", MaxFiles: 1, MaxFileBytes: 1 << 20})
	require.NoError(t, err)
	f1, err := d.Create()
	require.NoError(t, err)
	_, err = d.Create()
	require.ErrorIs(t, err, ErrNoCapacity)
	require.NoError(t, f1.Close())
	f2, err := d.Create()
	require.NoError(t, err)
	require.NoError(t, f2.Close())
}

func TestFile_BufferedReadableLen(t *testing.T) {
	d, err := Open(Config{Path: t.TempDir(), Prefix: "b-", MaxFiles: 2, MaxFileBytes: 1 << 20, WriteBuffer: 64 << 10})
	require.NoError(t, err)
	f, err := d.Create()
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	_, err = f.Write([]byte("abcdef"))
	require.NoError(t, err)
	require.Equal(t, int64(6), f.Len())
	require.Equal(t, int64(0), f.ReadableLen())
	require.NoError(t, f.Flush())
	require.Equal(t, int64(6), f.ReadableLen())

	r, err := f.Reader()
	require.NoError(t, err)
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "abcdef", string(got))
}

func TestFile_UnbufferedReadAtConcurrentWithWrite(t *testing.T) {
	d, err := Open(Config{Path: t.TempDir(), Prefix: "u-", MaxFiles: 2, MaxFileBytes: 1 << 20, WriteBuffer: 0})
	require.NoError(t, err)
	f, err := d.Create()
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	_, err = f.Write([]byte("0123456789"))
	require.NoError(t, err)
	require.Equal(t, int64(10), f.ReadableLen())

	buf := make([]byte, 4)
	n, err := f.ReadAt(buf, 2)
	require.NoError(t, err)
	require.Equal(t, 4, n)
	require.Equal(t, "2345", string(buf))

	_, err = f.Write([]byte("ABCD"))
	require.NoError(t, err)
	n, err = f.ReadAt(buf, 10)
	require.NoError(t, err)
	require.Equal(t, "ABCD", string(buf[:n]))
}

func TestFile_MaxFileBytes(t *testing.T) {
	d, err := Open(Config{Path: t.TempDir(), Prefix: "m-", MaxFiles: 1, MaxFileBytes: 4})
	require.NoError(t, err)
	f, err := d.Create()
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	_, err = f.Write([]byte("1234"))
	require.NoError(t, err)
	_, err = f.Write([]byte("x"))
	require.ErrorIs(t, err, ErrFileTooLarge)
}

func TestBudget_Reclassify(t *testing.T) {
	b := NewBudget(100, 200)
	require.NoError(t, b.ChargeRAM(40))
	require.NoError(t, b.ReclassifyToDisk(40))
	ram, disk := b.Stats()
	require.Equal(t, int64(0), ram)
	require.Equal(t, int64(40), disk)
	require.ErrorIs(t, b.ChargeDisk(200), ErrBudgetExceeded)
}

func TestSlots_SetMaxKeepsInFlight(t *testing.T) {
	s := NewSlots(2)
	require.True(t, s.TryAcquire())
	require.True(t, s.TryAcquire())
	require.False(t, s.TryAcquire())
	s.SetMax(1)
	max, cur := s.Stats()
	require.Equal(t, int64(1), max)
	require.Equal(t, int64(2), cur)
	require.False(t, s.TryAcquire())
	s.Release()
	s.Release()
	require.True(t, s.TryAcquire())
}

func TestSlots_UnlimitedSetMaxKeepsInFlight(t *testing.T) {
	s := NewSlots(0)
	require.True(t, s.TryAcquire())
	require.True(t, s.TryAcquire())
	max, cur := s.Stats()
	require.Equal(t, int64(0), max)
	require.Equal(t, int64(2), cur, "unlimited still tracks holders")

	s.SetMax(1)
	require.False(t, s.TryAcquire(), "in-flight unlimited holders count against the new cap")

	s.Release()
	max, cur = s.Stats()
	require.Equal(t, int64(1), max)
	require.Equal(t, int64(1), cur)
	require.False(t, s.TryAcquire(), "one remaining holder still occupies the cap")

	s.Release()
	require.True(t, s.TryAcquire())
	require.False(t, s.TryAcquire())
	s.Release()
}

func TestCreate_UnlimitedTracksFilesOpen(t *testing.T) {
	d, err := Open(Config{
		Path: t.TempDir(), Prefix: "u-", MaxFiles: 0, MaxFileBytes: 1 << 20, AllowUnlimited: true,
	})
	require.NoError(t, err)
	f1, err := d.Create()
	require.NoError(t, err)
	defer func() { _ = f1.Close() }()
	require.Equal(t, int64(1), d.Stats().FilesOpen)

	snap := d.Snapshot()
	require.NoError(t, d.Reconfigure(Config{
		Path: snap.Path, Prefix: snap.Prefix, MaxFiles: 1, MaxFileBytes: snap.MaxFileBytes,
	}))
	_, err = d.Create()
	require.ErrorIs(t, err, ErrNoCapacity)
	require.NoError(t, f1.Close())
	f2, err := d.Create()
	require.NoError(t, err)
	require.NoError(t, f2.Close())
}

func TestBuffer_SpillAndRoundTrip(t *testing.T) {
	d, err := Open(Config{Path: t.TempDir(), Prefix: "buf-", MaxFiles: 4, MaxFileBytes: 1 << 20, WriteBuffer: 64 << 10})
	require.NoError(t, err)
	buf := NewBuffer(BufferConfig{
		Dir:            d,
		Budget:         NewBudget(8, 1<<20),
		OnSpillFailure: FailRequest,
	})
	defer func() { _ = buf.Close() }()

	_, err = buf.Write([]byte("hello"))
	require.NoError(t, err)
	require.False(t, buf.Spilled())
	_, err = buf.Write(bytes.Repeat([]byte("x"), 10))
	require.NoError(t, err)
	require.True(t, buf.Spilled())

	entries, err := os.ReadDir(d.Snapshot().Path)
	require.NoError(t, err)
	require.Empty(t, entries)

	got, err := buf.Bytes()
	require.NoError(t, err)
	require.Equal(t, "hello"+string(bytes.Repeat([]byte("x"), 10)), string(got))
}

func TestBuffer_DegradeToRAM(t *testing.T) {
	d, err := Open(Config{
		Path: t.TempDir(), Prefix: "e-", MaxFiles: 1, MaxFileBytes: 64, WriteBuffer: 1024,
	})
	require.NoError(t, err)
	blocker, err := d.Create()
	require.NoError(t, err)
	defer func() { _ = blocker.Close() }()

	deg := NewSlots(1)
	buf := NewBuffer(BufferConfig{
		Dir:            d,
		Budget:         NewBudget(4, 64),
		OnSpillFailure: DegradeToRAM,
		Degraded:       deg,
	})
	defer func() { _ = buf.Close() }()
	_, err = buf.Write([]byte("12345"))
	require.NoError(t, err)
	require.False(t, buf.Spilled(), "spill refused; stayed in RAM under degrade")
	require.Equal(t, int64(64), buf.DiskLimit())
	_, cur := deg.Stats()
	require.Equal(t, int64(1), cur, "degraded slot held")
}

func TestIndex_AppendAt(t *testing.T) {
	d, err := Open(Config{Path: t.TempDir(), Prefix: "i-", MaxFiles: 2, MaxFileBytes: 1 << 20})
	require.NoError(t, err)
	f, err := d.Create()
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	idx, err := d.CreateIndex()
	require.NoError(t, err)
	defer func() { _ = idx.Close() }()

	_, err = f.Write([]byte("aaabbbccc"))
	require.NoError(t, err)
	require.NoError(t, idx.Append([]int64{0, 3, 6}))
	require.Equal(t, int64(3), idx.Len())
	off, err := idx.At(1)
	require.NoError(t, err)
	require.Equal(t, int64(3), off)
	_, err = idx.At(3)
	require.ErrorIs(t, err, ErrIndexPast)
}
