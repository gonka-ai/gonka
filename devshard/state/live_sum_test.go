package state

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/gtank/ristretto255"
	"github.com/stretchr/testify/require"
)

func TestExpandMessageXMD_RFC9380SHA256(t *testing.T) {
	dst := []byte("QUUX-V01-CS02-with-expander-SHA256-128")
	empty32 := expandMessageXMD(sha256.New, sha256.BlockSize, nil, dst, 32)
	require.Equal(t, "68a985b87eb6b46952128911f2a4412bbc302a9d759667f87f7a21d803f07235", hex.EncodeToString(empty32))

	// len_in_bytes = 0x80 exercises ell > 1.
	empty128 := expandMessageXMD(sha256.New, sha256.BlockSize, nil, dst, 128)
	require.Equal(t,
		"af84c27ccfd45d41914fdff5df25293e221afc53d8ad2ac06d5e3e29485dadbee0d121587713a3e0dd4d5e69e93eb7cd4f5df4cd103e188cf60cb02edc3edf18eda8576c412b18ffb658e3dd6ec849469b979d444cf7b26911a08e63cf31f9dcc541708d3491184472c2c29bb749d4286b004ceb5ee6b9a7fa5b646c993f0ced",
		hex.EncodeToString(empty128))
}

func TestLivePointIdentityAndOrder(t *testing.T) {
	empty := sumLivePointsFromEntries(nil)
	require.Equal(t, [32]byte{}, encodeLivePoint(&empty))
	empty = sumLivePointsFromEntries(map[uint64][]byte{})
	require.Equal(t, [32]byte{}, encodeLivePoint(&empty))

	a := []byte("record-a")
	b := []byte("record-b")
	c := []byte("record-c")
	forward := sumLivePointsFromEntries(map[uint64][]byte{1: a, 2: b, 3: c})
	backward := sumLivePointsFromEntries(map[uint64][]byte{3: c, 1: a, 2: b})
	require.Equal(t, encodeLivePoint(&forward), encodeLivePoint(&backward))

	var added ristretto255.Element
	added.Zero()
	addLivePoint(&added, a)
	addLivePoint(&added, b)
	addLivePoint(&added, c)
	require.Equal(t, encodeLivePoint(&forward), encodeLivePoint(&added))

	withoutB := forward
	subLivePoint(&withoutB, b)
	rest := sumLivePointsFromEntries(map[uint64][]byte{1: a, 3: c})
	require.Equal(t, encodeLivePoint(&rest), encodeLivePoint(&withoutB))

	var one ristretto255.Element
	one.Zero()
	addLivePoint(&one, a)
	subLivePoint(&one, a)
	require.Equal(t, [32]byte{}, encodeLivePoint(&one))

	replaced := forward
	subLivePoint(&replaced, b)
	addLivePoint(&replaced, []byte("record-b-prime"))
	want := sumLivePointsFromEntries(map[uint64][]byte{
		1: a, 2: []byte("record-b-prime"), 3: c,
	})
	require.Equal(t, encodeLivePoint(&want), encodeLivePoint(&replaced))
	require.NotEqual(t, encodeLivePoint(&forward), encodeLivePoint(&replaced))
}

func TestLiveEntryPointKnownAnswer(t *testing.T) {
	// Frozen so a DST, framing, or hash-to-curve change fails closed.
	got := encodeLivePoint(liveEntryPoint([]byte("devshard-live-set-kat")))
	var sum ristretto255.Element
	sum.Zero()
	addLivePoint(&sum, []byte("left"))
	addLivePoint(&sum, []byte("right"))
	encoded := encodeLivePoint(&sum)
	require.Equal(t, "165a952d0a282c3595c432451c4b4642de1f764059a00eaf87e3c6644e671679", hex.EncodeToString(got[:]))
	require.Equal(t, "92c963f8e881ac9c23bdc987e15f0f9f199dd94eb013ba1f9bd4abfd57f55d19", hex.EncodeToString(encoded[:]))
}

// TestLiveSumRejectsXORCollision is the attack on the old combiner.
// Two variants per id give difference vectors in GF(2)^256. Past 256 ids,
// Gaussian elimination finds a subset of flips that leaves the XOR unchanged.
// The point sum of those two sequences must differ.
func TestLiveSumRejectsXORCollision(t *testing.T) {
	const n = 320
	diffs := make([][32]byte, n)
	variantA := make([][]byte, n)
	variantB := make([][]byte, n)
	for i := 0; i < n; i++ {
		variantA[i] = []byte{byte(i), byte(i >> 8), 'A'}
		variantB[i] = []byte{byte(i), byte(i >> 8), 'B'}
		diffs[i] = xor32(shaFrame(variantA[i]), shaFrame(variantB[i]))
	}
	flip := xorKernel(diffs)
	require.NotNil(t, flip, "expected a linear dependence among %d random differences", n)

	allA := make(map[uint64][]byte, n)
	flipped := make(map[uint64][]byte, n)
	var xorA, xorFlip [32]byte
	flips := 0
	for i := 0; i < n; i++ {
		id := uint64(i + 1)
		allA[id] = variantA[i]
		xorA = xor32(xorA, shaFrame(variantA[i]))
		if flip[i] == 1 {
			flipped[id] = variantB[i]
			xorFlip = xor32(xorFlip, shaFrame(variantB[i]))
			flips++
		} else {
			flipped[id] = variantA[i]
			xorFlip = xor32(xorFlip, shaFrame(variantA[i]))
		}
	}
	require.NotZero(t, flips)
	require.Equal(t, xorA, xorFlip, "the constructed sequences must share an XOR")
	sumA := sumLivePointsFromEntries(allA)
	sumFlip := sumLivePointsFromEntries(flipped)
	require.NotEqual(t, encodeLivePoint(&sumA), encodeLivePoint(&sumFlip))
}

func shaFrame(entry []byte) [32]byte {
	return sha256.Sum256(frameEntry(entry))
}

func xor32(a, b [32]byte) [32]byte {
	for i := range a {
		a[i] ^= b[i]
	}
	return a
}

func bitAt(d [32]byte, i int) byte {
	return (d[i/8] >> (i % 8)) & 1
}

// xorKernel returns a non-zero 0/1 vector x such that XOR_i x_i * row_i = 0,
// or nil when the rows are independent.
func xorKernel(rows [][32]byte) []byte {
	n := len(rows)
	work := append([][32]byte(nil), rows...)
	comb := make([][]byte, n)
	for i := range comb {
		comb[i] = make([]byte, n)
		comb[i][i] = 1
	}
	used := make([]bool, n)
	for bit := 0; bit < 256; bit++ {
		piv := -1
		for r := 0; r < n; r++ {
			if used[r] {
				continue
			}
			if bitAt(work[r], bit) == 1 {
				piv = r
				break
			}
		}
		if piv < 0 {
			continue
		}
		used[piv] = true
		for r := 0; r < n; r++ {
			if r == piv || bitAt(work[r], bit) == 0 {
				continue
			}
			work[r] = xor32(work[r], work[piv])
			for i := 0; i < n; i++ {
				comb[r][i] ^= comb[piv][i]
			}
		}
	}
	for r := 0; r < n; r++ {
		if work[r] != ([32]byte{}) {
			continue
		}
		for _, b := range comb[r] {
			if b != 0 {
				return comb[r]
			}
		}
	}
	return nil
}
