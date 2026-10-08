package state

import (
	"crypto/sha256"
	"encoding"
	"fmt"
	"hash"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	// benchSealBatch is one auto-seal that removes the oldest live ids.
	// Step 2 must rebuild every midstate after that prefix. Merkle and XOR update those ids only.
	benchSealBatch = 128
	// benchMixDiffs is one auto-seal interval. Each diff appends. Every other diff also
	// rewrites an id in the older half. The last diff seals the oldest benchSealBatch ids.
	benchMixDiffs = 150
)

var (
	benchDigestSink [32]byte
	benchNodeSink   *merkleNode
	benchBytesSink  []byte
)

// BenchmarkCommitmentUpdate measures one live-set edit at 10^3, 10^4, and the
// 30-minute size (28_800). Step 2 resumes SHA-256 from the first edited frame
// and rewrites midstates for the suffix. The Merkle treap and the XOR each
// update only the ids that changed. All three read frames that are already in
// memory, the same bytes committedEntries holds.
func BenchmarkCommitmentUpdate(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 30 * 60 * 32 / 2} {
		frames := benchFrames(b, n+benchMixDiffs)
		base := frames[:n]
		extra := frames[n : n+benchMixDiffs]
		tail := append([]byte(nil), base[n-5]...)
		tail[len(tail)-1] ^= 0xff
		mid := append([]byte(nil), base[n/2]...)
		mid[len(mid)-1] ^= 0xff
		older := make([][]byte, benchMixDiffs/2)
		for i := range older {
			f := append([]byte(nil), base[(i*17)%(n/2)]...)
			f[len(f)-1] ^= 0xff
			older[i] = f
		}

		s2 := newStep2(base)
		xs := newXORSet(base)
		tree := newMerkle(base)
		if s2.digest != hashFrames(base) {
			b.Fatal("step2 digest diverged from full concatenation")
		}
		if got := s2.digestFrom(n-5, tail); got != hashFrames(replaced(base, n-5, tail)) {
			b.Fatal("step2 tail replace diverged")
		}
		if got := s2.digestFrom(n/2, mid); got != hashFrames(replaced(base, n/2, mid)) {
			b.Fatal("step2 mid replace diverged")
		}
		if got := s2.digestDrop(benchSealBatch); got != hashFrames(base[benchSealBatch:]) {
			b.Fatal("step2 seal diverged")
		}
		if h := tree.height(); h > 4*bitsLen(n) {
			b.Fatalf("merkle treap height %d is degenerate for n=%d", h, n)
		}
		fresh := (*merkleNode)(nil)
		for i, f := range base[benchSealBatch:] {
			fresh = fresh.insert(uint64(benchSealBatch+1+i), sha256.Sum256(f))
		}
		if merkleRoot(merkleDrop(tree, benchSealBatch)) != merkleRoot(fresh) {
			b.Fatal("merkle seal diverged from a fresh tree of the surviving ids")
		}
		if xs.drop(benchSealBatch) != newXORSet(base[benchSealBatch:]).acc {
			b.Fatal("xor seal diverged")
		}

		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			run := func(op, scheme string, extraBytes int, fn func(b *testing.B)) {
				b.Run(op+"/"+scheme, func(b *testing.B) {
					b.ReportAllocs()
					b.ReportMetric(float64(extraBytes), "extra-B")
					fn(b)
				})
			}
			run("append", "step2", s2.extraBytes(), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					benchDigestSink = s2.appendDigest(extra[0])
				}
			})
			run("append", "merkle", tree.extraBytes(), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					benchNodeSink = tree.insert(uint64(n+1), sha256.Sum256(extra[0]))
				}
			})
			run("append", "xor", xs.extraBytes(), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					benchDigestSink = xs.append(extra[0])
				}
			})

			run("replace-tail", "step2", s2.extraBytes(), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					benchDigestSink = s2.digestFrom(n-5, tail)
				}
			})
			run("replace-tail", "merkle", tree.extraBytes(), func(b *testing.B) {
				id := uint64(n - 4)
				leaf := sha256.Sum256(tail)
				for i := 0; i < b.N; i++ {
					benchNodeSink = tree.replace(id, leaf)
				}
			})
			run("replace-tail", "xor", xs.extraBytes(), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					benchDigestSink = xs.replace(n-5, tail)
				}
			})

			run("replace-mid", "step2", s2.extraBytes(), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					benchDigestSink = s2.digestFrom(n/2, mid)
				}
			})
			run("replace-mid", "merkle", tree.extraBytes(), func(b *testing.B) {
				id := uint64(n/2 + 1)
				leaf := sha256.Sum256(mid)
				for i := 0; i < b.N; i++ {
					benchNodeSink = tree.replace(id, leaf)
				}
			})
			run("replace-mid", "xor", xs.extraBytes(), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					benchDigestSink = xs.replace(n/2, mid)
				}
			})

			run("seal-oldest", "step2", s2.extraBytes(), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					benchDigestSink = s2.digestDrop(benchSealBatch)
				}
			})
			run("seal-oldest", "merkle", tree.extraBytes(), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					benchNodeSink = merkleDrop(tree, benchSealBatch)
				}
			})
			run("seal-oldest", "xor", xs.extraBytes(), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					benchDigestSink = xs.drop(benchSealBatch)
				}
			})

			run("mix", "step2", s2.extraBytes(), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					benchDigestSink = s2.mix(extra, older)
				}
			})
			run("mix", "merkle", tree.extraBytes(), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					benchNodeSink = tree.mix(n, extra, older)
				}
			})
			run("mix", "xor", xs.extraBytes(), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					benchDigestSink = xs.mix(n, extra, older)
				}
			})
		})
	}
}

func TestCommitmentUpdatesMatchFullRebuild(t *testing.T) {
	const n = 300
	frames := benchFrames(&testing.B{}, n)
	k := 40
	s2 := newStep2(frames)
	if s2.digest != hashFrames(frames) {
		t.Fatal("step2 digest")
	}
	mut := append([]byte(nil), frames[n/2]...)
	mut[len(mut)-1] ^= 0xff
	if s2.digestFrom(n/2, mut) != hashFrames(replaced(frames, n/2, mut)) {
		t.Fatal("step2 replace")
	}
	if s2.appendDigest(mut) != hashFrames(append(append([][]byte{}, frames...), mut)) {
		t.Fatal("step2 append")
	}
	if s2.digestDrop(k) != hashFrames(frames[k:]) {
		t.Fatal("step2 seal")
	}

	tree := newMerkle(frames)
	if h := tree.height(); h > 4*bitsLen(n) {
		t.Fatalf("height %d", h)
	}
	fresh := (*merkleNode)(nil)
	for i, f := range frames[k:] {
		fresh = fresh.insert(uint64(k+1+i), sha256.Sum256(f))
	}
	if merkleRoot(merkleDrop(tree, k)) != merkleRoot(fresh) {
		t.Fatal("merkle seal")
	}
	leaf := sha256.Sum256(mut)
	if merkleRoot(tree.replace(uint64(n/2+1), leaf)) == merkleRoot(tree) {
		t.Fatal("merkle replace left the root unchanged")
	}

	xs := newXORSet(frames)
	if xs.drop(k) != newXORSet(frames[k:]).acc {
		t.Fatal("xor seal")
	}
	if xs.append(mut) == xs.acc {
		t.Fatal("xor append left the accumulator unchanged")
	}
}

func benchFrames(b *testing.B, n int) [][]byte {
	b.Helper()
	_, entries := benchFinishedSet(b, n)
	frames := make([][]byte, n)
	for i := 0; i < n; i++ {
		frames[i] = frameEntry(entries[uint64(i+1)])
	}
	return frames
}

func frameEntry(entry []byte) []byte {
	buf := make([]byte, 0, len(entry)+4)
	buf = protowire.AppendTag(buf, 1, protowire.BytesType)
	buf = protowire.AppendVarint(buf, uint64(len(entry)))
	return append(buf, entry...)
}

func hashFrames(frames [][]byte) [32]byte {
	h := sha256.New()
	for _, f := range frames {
		h.Write(f)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func replaced(frames [][]byte, i int, frame []byte) [][]byte {
	out := append([][]byte(nil), frames...)
	out[i] = frame
	return out
}

type step2 struct {
	frames [][]byte
	points [][]byte
	digest [32]byte
}

func newStep2(frames [][]byte) *step2 {
	h := sha256.New()
	points := make([][]byte, len(frames)+1)
	points[0] = mustMarshalHash(h)
	for i, f := range frames {
		h.Write(f)
		points[i+1] = mustMarshalHash(h)
	}
	s := &step2{frames: frames, points: points}
	copy(s.digest[:], h.Sum(nil))
	return s
}

func (s *step2) extraBytes() int {
	n := 0
	for _, p := range s.points {
		n += len(p)
	}
	return n
}

func (s *step2) appendDigest(frame []byte) [32]byte {
	h := sha256.New()
	mustUnmarshal(h, s.points[len(s.frames)])
	h.Write(frame)
	benchBytesSink = mustMarshalHash(h)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// digestFrom resumes at index i, writes frame in place of frames[i], then the
// untouched suffix, and stores a midstate after every rewritten frame.
func (s *step2) digestFrom(i int, frame []byte) [32]byte {
	h := sha256.New()
	mustUnmarshal(h, s.points[i])
	h.Write(frame)
	benchBytesSink = mustMarshalHash(h)
	for _, f := range s.frames[i+1:] {
		h.Write(f)
		benchBytesSink = mustMarshalHash(h)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// digestDrop rebuilds the concatenation of the frames that survive a prefix seal.
// Midstates before the cut include the deleted frames, so none of them can be resumed.
func (s *step2) digestDrop(k int) [32]byte {
	h := sha256.New()
	benchBytesSink = mustMarshalHash(h)
	for _, f := range s.frames[k:] {
		h.Write(f)
		benchBytesSink = mustMarshalHash(h)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func (s *step2) mix(extra [][]byte, older [][]byte) [32]byte {
	frames := make([][]byte, len(s.frames), len(s.frames)+len(extra))
	copy(frames, s.frames)
	points := make([][]byte, len(s.points), len(s.points)+len(extra))
	copy(points, s.points)
	cur := &step2{frames: frames, points: points, digest: s.digest}
	vote := 0
	for i, frame := range extra {
		cur.appendInPlace(frame)
		if i%2 == 0 {
			idx := (i * 17) % (len(s.frames) / 2)
			cur.replaceInPlace(idx, older[vote])
			vote++
		}
	}
	cur.dropInPlace(benchSealBatch)
	return cur.digest
}

func (s *step2) appendInPlace(frame []byte) {
	h := sha256.New()
	mustUnmarshal(h, s.points[len(s.frames)])
	h.Write(frame)
	s.points = append(s.points, mustMarshalHash(h))
	s.frames = append(s.frames, frame)
	copy(s.digest[:], h.Sum(nil))
}

func (s *step2) replaceInPlace(i int, frame []byte) {
	h := sha256.New()
	mustUnmarshal(h, s.points[i])
	s.frames[i] = frame
	for j := i; j < len(s.frames); j++ {
		h.Write(s.frames[j])
		s.points[j+1] = mustMarshalHash(h)
	}
	copy(s.digest[:], h.Sum(nil))
}

func (s *step2) dropInPlace(k int) {
	h := sha256.New()
	frames := s.frames[k:]
	points := make([][]byte, len(frames)+1)
	points[0] = mustMarshalHash(h)
	for i, f := range frames {
		h.Write(f)
		points[i+1] = mustMarshalHash(h)
	}
	s.frames = frames
	s.points = points
	copy(s.digest[:], h.Sum(nil))
}

type xorSet struct {
	acc     [32]byte
	digests [][32]byte
}

func newXORSet(frames [][]byte) *xorSet {
	x := &xorSet{digests: make([][32]byte, len(frames))}
	for i, f := range frames {
		x.digests[i] = sha256.Sum256(f)
		xorBytes(&x.acc, &x.digests[i])
	}
	return x
}

func (x *xorSet) extraBytes() int { return len(x.digests) * 32 }

func (x *xorSet) append(frame []byte) [32]byte {
	d := sha256.Sum256(frame)
	acc := x.acc
	xorBytes(&acc, &d)
	return acc
}

func (x *xorSet) replace(i int, frame []byte) [32]byte {
	d := sha256.Sum256(frame)
	acc := x.acc
	xorBytes(&acc, &x.digests[i])
	xorBytes(&acc, &d)
	return acc
}

func (x *xorSet) drop(k int) [32]byte {
	acc := x.acc
	for i := 0; i < k; i++ {
		xorBytes(&acc, &x.digests[i])
	}
	return acc
}

func (x *xorSet) mix(baseN int, extra [][]byte, older [][]byte) [32]byte {
	acc := x.acc
	vote := 0
	for i, frame := range extra {
		d := sha256.Sum256(frame)
		xorBytes(&acc, &d)
		if i%2 == 0 {
			idx := (i * 17) % (baseN / 2)
			old := x.digests[idx]
			xorBytes(&acc, &old)
			neu := sha256.Sum256(older[vote])
			xorBytes(&acc, &neu)
			vote++
		}
	}
	for i := 0; i < benchSealBatch; i++ {
		xorBytes(&acc, &x.digests[i])
	}
	return acc
}

func xorBytes(dst, src *[32]byte) {
	for i := range dst {
		dst[i] ^= src[i]
	}
}

// merkleNode is one node of a deterministic treap ordered by inference id.
// The node hash is sha256 of the left hash, this leaf, and the right hash,
// so the root is a sorted Merkle commitment of the live frames.
type merkleNode struct {
	id          uint64
	pri         uint64
	leaf        [32]byte
	left, right *merkleNode
	hash        [32]byte
}

func newMerkle(frames [][]byte) *merkleNode {
	var root *merkleNode
	for i, f := range frames {
		root = root.insert(uint64(i+1), sha256.Sum256(f))
	}
	return root
}

func (t *merkleNode) extraBytes() int {
	var n int
	var walk func(*merkleNode)
	walk = func(n0 *merkleNode) {
		if n0 == nil {
			return
		}
		n++
		walk(n0.left)
		walk(n0.right)
	}
	walk(t)
	return n * int(merkleNodeBytes)
}

const merkleNodeBytes = 8 + 8 + 32 + 16 + 32 // id, pri, leaf, two pointers, hash

func (t *merkleNode) insert(id uint64, leaf [32]byte) *merkleNode {
	n := &merkleNode{id: id, pri: splitmix64(id), leaf: leaf}
	n.hash = merkleNodeHash(nil, &n.leaf, nil)
	left, right := treapSplit(t, id)
	return treapMerge(treapMerge(left, n), right)
}

func (t *merkleNode) replace(id uint64, leaf [32]byte) *merkleNode {
	left, rest := treapSplit(t, id)
	_, right := treapSplit(rest, id+1)
	n := &merkleNode{id: id, pri: splitmix64(id), leaf: leaf}
	n.hash = merkleNodeHash(nil, &n.leaf, nil)
	return treapMerge(treapMerge(left, n), right)
}

func merkleDrop(root *merkleNode, k int) *merkleNode {
	for id := uint64(1); id <= uint64(k); id++ {
		root = root.remove(id)
	}
	return root
}

func (t *merkleNode) remove(id uint64) *merkleNode {
	left, rest := treapSplit(t, id)
	_, right := treapSplit(rest, id+1)
	return treapMerge(left, right)
}

func (t *merkleNode) mix(baseN int, extra [][]byte, older [][]byte) *merkleNode {
	root := t
	vote := 0
	for i, frame := range extra {
		id := uint64(baseN + 1 + i)
		root = root.insert(id, sha256.Sum256(frame))
		if i%2 == 0 {
			idx := (i * 17) % (baseN / 2)
			root = root.replace(uint64(idx+1), sha256.Sum256(older[vote]))
			vote++
		}
	}
	return merkleDrop(root, benchSealBatch)
}

func (t *merkleNode) height() int {
	maxd := 0
	stack := []struct {
		n *merkleNode
		d int
	}{{t, 1}}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if f.n == nil {
			continue
		}
		if f.d > maxd {
			maxd = f.d
		}
		stack = append(stack, struct {
			n *merkleNode
			d int
		}{f.n.left, f.d + 1}, struct {
			n *merkleNode
			d int
		}{f.n.right, f.d + 1})
	}
	return maxd
}

func bitsLen(n int) int {
	b := 0
	for n > 0 {
		n >>= 1
		b++
	}
	return b
}

func merkleRoot(t *merkleNode) [32]byte {
	if t == nil {
		return [32]byte{}
	}
	return t.hash
}

func treapSplit(t *merkleNode, id uint64) (*merkleNode, *merkleNode) {
	if t == nil {
		return nil, nil
	}
	if t.id < id {
		n := *t
		left, right := treapSplit(t.right, id)
		n.right = left
		n.hash = merkleNodeHash(n.left, &n.leaf, n.right)
		return &n, right
	}
	n := *t
	left, right := treapSplit(t.left, id)
	n.left = right
	n.hash = merkleNodeHash(n.left, &n.leaf, n.right)
	return left, &n
}

func treapMerge(a, b *merkleNode) *merkleNode {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if a.pri > b.pri {
		n := *a
		n.right = treapMerge(a.right, b)
		n.hash = merkleNodeHash(n.left, &n.leaf, n.right)
		return &n
	}
	n := *b
	n.left = treapMerge(a, b.left)
	n.hash = merkleNodeHash(n.left, &n.leaf, n.right)
	return &n
}

func merkleNodeHash(left *merkleNode, leaf *[32]byte, right *merkleNode) [32]byte {
	var buf [1 + 32 + 32 + 1 + 32]byte
	if left != nil {
		buf[0] = 1
		copy(buf[1:33], left.hash[:])
	}
	copy(buf[33:65], leaf[:])
	if right != nil {
		buf[65] = 1
		copy(buf[66:98], right.hash[:])
	}
	return sha256.Sum256(buf[:])
}

func splitmix64(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}

func mustMarshalHash(h hash.Hash) []byte {
	st, err := h.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		panic(err)
	}
	return st
}

func mustUnmarshal(h hash.Hash, st []byte) {
	if err := h.(encoding.BinaryUnmarshaler).UnmarshalBinary(st); err != nil {
		panic(err)
	}
}
