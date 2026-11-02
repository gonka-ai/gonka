package artifacts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func assertSnapshotProofs(t *testing.T, s *SMSTArtifactStore, count uint32, root []byte, depth uint32) {
	t.Helper()
	entries, err := s.GetArtifactsAndProofs([]uint32{0, count - 1}, count)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !VerifySMSTProofWithDenseIndex(root, count, depth, e.DenseIndex, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
			t.Fatalf("unprovable tuple count=%d depth=%d nonce=%d", count, depth, e.Nonce)
		}
		if VerifySMSTProofWithDenseIndex(root, count, depth+1, e.DenseIndex, e.Nonce, encodeLeaf(e.Nonce, e.Vector), e.Proof) {
			t.Fatal("accepted mismatched depth")
		}
	}
}

func TestSMSTDepthGrowthSnapshots(t *testing.T) {
	for _, limit := range []uint32{2, 1_000_000} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			dir := t.TempDir()
			s := openSMSTWithRAMLimit(t, dir, limit)

			nonces := []int32{1, 2, 3, 1 << 24, 1 << 30, -2147483648}
			depths := []uint32{24, 24, 24, 25, 31, 32}
			roots := make(map[uint32][]byte)
			for i, n := range nonces {
				if err := s.AddWithNode(n, []byte("vector"), "node"); err != nil {
					t.Fatal(err)
				}
				if err := s.Flush(); err != nil {
					t.Fatal(err)
				}
				c, r, d, err := s.GetFlushedSnapshot()
				if err != nil || c != uint32(i+1) || d != depths[i] {
					t.Fatalf("tuple %d/%d err=%v", c, d, err)
				}
				roots[c] = r

				r[0] ^= 0xff
				_, r2, _, _ := s.GetFlushedSnapshot()
				if bytes.Equal(r, r2) {
					t.Fatal("root aliases store")
				}
				r[0] ^= 0xff
				assertSnapshotProofs(t, s, c, r, d)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}

			path := filepath.Join(dir, "flushed_roots.jsonl")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var legacy []byte
			for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
				var entry flushedRootEntry
				if err := json.Unmarshal(line, &entry); err != nil {
					t.Fatal(err)
				}
				entry.Depth = nil
				b, _ := json.Marshal(entry)
				legacy = append(legacy, append(b, '\n')...)
			}

			legacy = append(legacy, []byte(`{"count":7,"root":"`)...)
			if err := os.WriteFile(path, legacy, 0644); err != nil {
				t.Fatal(err)
			}
			var migratedMetadata []byte
			for reopen := 0; reopen < 2; reopen++ {
				s = openSMSTWithRAMLimit(t, dir, limit)
				c, r, d, err := s.GetFlushedSnapshot()
				if err != nil || c != 6 || d != 32 || !bytes.Equal(r, roots[6]) {
					t.Fatalf("reopened tuple %d/%d err=%v", c, d, err)
				}

				for i := range nonces {
					count := uint32(i + 1)
					assertSnapshotProofs(t, s, count, roots[count], depths[i])
				}
				s.mu.Lock()
				s.retained = make(map[uint32]smstSnapshot)
				s.mu.Unlock()
				globalSnapshotCache.purgeStore(s)
				for i := range nonces {
					assertSnapshotProofs(t, s, uint32(i+1), roots[uint32(i+1)], depths[i])
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				metadata, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if reopen == 0 {
					migratedMetadata = metadata
				} else if !bytes.Equal(migratedMetadata, metadata) {
					t.Fatal("legacy depth backfill repeated on a second restart")
				}
			}
		})
	}
}

func TestSMSTSnapshotPublicationRetry(t *testing.T) {
	s := openSMSTWithRAMLimit(t, t.TempDir(), 1_000_000)
	defer s.Close()
	if err := s.AddWithNode(1, []byte("vector"), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	oldCount, oldRoot, oldDepth, _ := s.GetFlushedSnapshot()
	if err := s.AddWithNode(1<<24, []byte("vector"), ""); err != nil {
		t.Fatal(err)
	}
	roots := s.rootsFile
	if err := roots.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err == nil {
		t.Fatal("expected root append failure")
	}
	c, r, d, err := s.GetFlushedSnapshot()
	if err != nil || c != oldCount || d != oldDepth || !bytes.Equal(r, oldRoot) {
		t.Fatal("failed flush published new tuple")
	}
	assertSnapshotProofs(t, s, c, r, d)
	s.rootsFile, err = os.OpenFile(filepath.Join(s.dir, "flushed_roots.jsonl"), os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	c, r, d, err = s.GetFlushedSnapshot()
	if err != nil || c != 2 || d != 25 {
		t.Fatalf("retry %d/%d: %v", c, d, err)
	}
	assertSnapshotProofs(t, s, c, r, d)
	s.mu.Lock()
	delete(s.flushedRoots, c)
	s.mu.Unlock()
	if _, _, _, err := s.GetFlushedSnapshot(); err == nil {
		t.Fatal("missing tuple silently fell back to live root")
	}
}

func TestSMSTSnapshotConcurrentPublication(t *testing.T) {
	s := openSMSTWithRAMLimit(t, t.TempDir(), 8)
	defer s.Close()
	if err := s.AddWithNode(0, []byte("vector"), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := int32(1); i < 30; i++ {
			n := i
			if i >= 10 {
				n += 1 << 24
			}
			if err := s.AddWithNode(n, []byte("vector"), ""); err != nil {
				t.Error(err)
				return
			}
			if err := s.Flush(); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 100; i++ {
		c, r, d, err := s.GetFlushedSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		if c > 0 {
			assertSnapshotProofs(t, s, c, r, d)
		}
	}
	wg.Wait()
}

func TestSMSTRejectsPersistedDepthMismatch(t *testing.T) {
	for _, limit := range []uint32{1, 1_000_000} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			dir := t.TempDir()
			s := openSMSTWithRAMLimit(t, dir, limit)
			if err := s.AddWithNode(1<<24, []byte("vector"), ""); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "flushed_roots.jsonl")
			data, _ := os.ReadFile(path)
			data = bytes.ReplaceAll(data, []byte(`"depth":25`), []byte(`"depth":24`))
			if err := os.WriteFile(path, data, 0644); err != nil {
				t.Fatal(err)
			}
			if reopened, err := OpenSMST(dir); err == nil {
				reopened.Close()
				t.Fatal("accepted conflicting depth")
			}
		})
	}
}

func BenchmarkSMSTFlushedSnapshot(b *testing.B) {
	s, err := OpenSMST(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	if err := s.AddWithNode(1, []byte("vector"), ""); err != nil {
		b.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := s.GetFlushedSnapshot(); err != nil {
			b.Fatal(err)
		}
	}
}

func TestSMSTAdmissionFixtures(t *testing.T) {
	data, err := os.ReadFile("../../../inference-chain/testutil/testdata/poc_depth_growth.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Nonce int32
		Count uint32
		Root  string
		Depth uint32
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	s := openSMSTWithRAMLimit(t, t.TempDir(), 2)
	defer s.Close()
	for _, f := range fixtures {
		if err := s.AddWithNode(f.Nonce, []byte("vector"), ""); err != nil {
			t.Fatal(err)
		}
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
		c, r, d, err := s.GetFlushedSnapshot()
		if err != nil || c != f.Count || fmt.Sprintf("%x", r) != f.Root || d != f.Depth {
			t.Fatalf("fixture mismatch at %d: %v", f.Count, err)
		}
	}
}

type failingRootRecord struct {
	*os.File
	shortWrite bool
	failSync   bool
	syncs      int
}

func (f *failingRootRecord) Write(b []byte) (int, error) {
	if f.shortWrite {
		f.shortWrite = false
		return f.File.Write(b[:len(b)/2])
	}
	return f.File.Write(b)
}
func (f *failingRootRecord) Sync() error {
	f.syncs++
	if f.failSync {
		f.failSync = false
		return fmt.Errorf("injected root fsync failure")
	}
	return f.File.Sync()
}
func TestSMSTPartialRootRecordRetry(t *testing.T) {
	for _, syncFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(syncFailure), func(t *testing.T) {
			s := openSMSTWithRAMLimit(t, t.TempDir(), 1_000_000)
			defer s.Close()
			if err := s.AddWithNode(1, []byte("vector"), ""); err != nil {
				t.Fatal(err)
			}
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
			raw := s.rootsFile.(*os.File)
			f := &failingRootRecord{File: raw, shortWrite: !syncFailure, failSync: syncFailure}
			s.rootsFile = f
			if err := s.AddWithNode(1<<24, []byte("vector"), ""); err != nil {
				t.Fatal(err)
			}
			if err := s.Flush(); err == nil {
				t.Fatal("injected error ignored")
			}
			c, r, d, err := s.GetFlushedSnapshot()
			if err != nil || c != 1 || d != 24 {
				t.Fatalf("published failed tuple %d/%d: %v", c, d, err)
			}
			assertSnapshotProofs(t, s, c, r, d)
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
			c, r, d, err = s.GetFlushedSnapshot()
			if err != nil || c != 2 || d != 25 {
				t.Fatalf("retry tuple %d/%d: %v", c, d, err)
			}
			assertSnapshotProofs(t, s, c, r, d)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openSMSTWithRAMLimit(t, s.dir, 1_000_000)
			defer reopened.Close()
			cc, rr, dd, err := reopened.GetFlushedSnapshot()
			if err != nil || cc != c || dd != d || !bytes.Equal(rr, r) {
				t.Fatal("partial record corrupted recovered tuple")
			}

			want := 1
			if syncFailure {
				want = 2
			}
			if f.syncs != want {
				t.Fatalf("root syncs=%d want=%d", f.syncs, want)
			}
		})
	}
}

func BenchmarkSMSTCommittedProofDepth(b *testing.B) {
	for _, nonce := range []int32{1, -2147483648} {
		s, err := OpenSMST(b.TempDir())
		if err != nil {
			b.Fatal(err)
		}
		if err := s.AddWithNode(0, []byte("vector"), ""); err != nil {
			b.Fatal(err)
		}
		if err := s.AddWithNode(nonce, []byte("vector"), ""); err != nil {
			b.Fatal(err)
		}
		if err := s.Flush(); err != nil {
			b.Fatal(err)
		}
		c, r, d, err := s.GetFlushedSnapshot()
		if err != nil {
			b.Fatal(err)
		}
		n, v, p, err := s.GetArtifactAndProof(0, c)
		if err != nil {
			b.Fatal(err)
		}
		leaf := encodeLeaf(n, v)
		b.Run(fmt.Sprint(d), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(p) * 36))
			for i := 0; i < b.N; i++ {
				if !VerifySMSTProofWithDenseIndex(r, c, d, 0, n, leaf, p) {
					b.Fatal("proof")
				}
			}
		})
		if err := s.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSMSTLegacyDepthRecovery(b *testing.B) {
	for _, limit := range []uint32{2, 1_000_000} {
		for _, boundaries := range []int{16, 64} {
			b.Run(fmt.Sprintf("limit_%d/boundaries_%d", limit, boundaries), func(b *testing.B) {
				b.Setenv(envSMSTRAMLeaves, fmt.Sprint(limit))
				dir := b.TempDir()
				s, err := OpenSMST(dir)
				if err != nil {
					b.Fatal(err)
				}
				for i := 0; i < boundaries; i++ {
					n := int32(i)
					if i >= boundaries/2 {
						n += 1 << 24
					}
					if err := s.AddWithNode(n, []byte("vector"), ""); err != nil {
						b.Fatal(err)
					}
					if err := s.Flush(); err != nil {
						b.Fatal(err)
					}
				}
				if err := s.Close(); err != nil {
					b.Fatal(err)
				}
				path := filepath.Join(dir, "flushed_roots.jsonl")
				data, err := os.ReadFile(path)
				if err != nil {
					b.Fatal(err)
				}
				var legacy []byte
				for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
					var entry flushedRootEntry
					if err := json.Unmarshal(line, &entry); err != nil {
						b.Fatal(err)
					}
					entry.Depth = nil
					record, _ := json.Marshal(entry)
					legacy = append(legacy, append(record, '\n')...)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					if err := os.WriteFile(path, legacy, 0644); err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
					s, err = OpenSMST(dir)
					if err != nil {
						b.Fatal(err)
					}
					if err := s.Close(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func TestSMSTInvalidDepthIsNotLegacy(t *testing.T) {
	for _, value := range []string{"0", "33", "-1", "4294967296", `"24"`} {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()
			s := openSMSTWithRAMLimit(t, dir, 1_000_000)
			if err := s.AddWithNode(1, []byte("vector"), ""); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "flushed_roots.jsonl")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = bytes.ReplaceAll(data, []byte(`"depth":24`), []byte(`"depth":`+value))
			if err := os.WriteFile(path, data, 0644); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenSMST(dir)
			if err == nil {
				reopened.Close()
				t.Fatal("invalid depth accepted as legacy missing metadata")
			}
		})
	}
}

func TestSMSTSpillPublicationFailureCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	s := openSMSTWithRAMLimit(t, dir, 2)
	if err := s.AddWithNode((1<<24)-1, []byte("vector"), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	oldCount, oldRoot, oldDepth, err := s.GetFlushedSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if oldDepth != 24 {
		t.Fatal(oldDepth)
	}
	failure := &failingRootRecord{File: s.rootsFile.(*os.File), shortWrite: true}
	s.rootsFile = failure
	if err := s.AddWithNode(1<<24, []byte("vector"), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err == nil {
		t.Fatal("expected failed publication during spill")
	}
	if !s.spilled {
		t.Fatal("expected paged transition")
	}
	count, root, depth, err := s.GetFlushedSnapshot()
	if err != nil || count != oldCount || depth != oldDepth || !bytes.Equal(root, oldRoot) {
		t.Fatal("spill changed the published tuple despite root journal failure")
	}
	assertSnapshotProofs(t, s, count, root, depth)
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	if err := s.dataFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.distFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.rootsFile.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openSMSTWithRAMLimit(t, dir, 2)
	defer reopened.Close()
	count, root, depth, err = reopened.GetFlushedSnapshot()
	if err != nil || count != 2 || depth != 25 {
		t.Fatalf("recovered tuple %d/%d err=%v", count, depth, err)
	}
	assertSnapshotProofs(t, reopened, oldCount, oldRoot, oldDepth)
	assertSnapshotProofs(t, reopened, count, root, depth)
}
