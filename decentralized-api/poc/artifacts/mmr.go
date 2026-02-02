package artifacts

import (
	"bytes"
	"crypto/sha256"
	"math/bits"
)

const (
	leafPrefix     = 0x00
	internalPrefix = 0x01
)

func hashLeaf(data []byte) []byte {
	h := sha256.New()
	h.Write([]byte{leafPrefix})
	h.Write(data)
	return h.Sum(nil)
}

func hashNode(left, right []byte) []byte {
	h := sha256.New()
	h.Write([]byte{internalPrefix})
	h.Write(left)
	h.Write(right)
	return h.Sum(nil)
}

// appendToMMR appends a leaf hash to the MMR nodes slice.
// leafIndex is the 0-based index of the leaf being added.
func appendToMMR(nodes *[][]byte, leafHash []byte, leafIndex uint32) {
	*nodes = append(*nodes, leafHash)
	currentHash := leafHash

	// Number of merges = trailing zeros in (leafIndex + 1)
	newLeafCount := leafIndex + 1
	merges := bits.TrailingZeros32(newLeafCount)

	for i := 0; i < merges; i++ {
		siblingOffset := (1 << (i + 1)) - 1
		siblingPos := len(*nodes) - 1 - siblingOffset

		if siblingPos < 0 {
			break
		}

		siblingHash := (*nodes)[siblingPos]
		parentHash := hashNode(siblingHash, currentHash)
		*nodes = append(*nodes, parentHash)
		currentHash = parentHash
	}
}

// mmrSizeForLeaves returns the total number of MMR nodes for n leaves.
// Formula: 2n - popcount(n)
func mmrSizeForLeaves(n uint32) int {
	if n == 0 {
		return 0
	}
	return int(2*n) - bits.OnesCount32(n)
}

// bagPeaks computes the root hash by bagging peaks from right to left.
func bagPeaks(nodes [][]byte, leafCount uint32) []byte {
	if leafCount == 0 {
		return nil
	}

	peaks := getPeaks(nodes, leafCount)
	if len(peaks) == 0 {
		return nil
	}

	root := peaks[len(peaks)-1]
	for i := len(peaks) - 2; i >= 0; i-- {
		root = hashNode(peaks[i], root)
	}
	return root
}

// getPeaks returns the peak hashes for an MMR with the given leaf count.
func getPeaks(nodes [][]byte, leafCount uint32) [][]byte {
	if leafCount == 0 {
		return nil
	}

	peaks := make([][]byte, 0, bits.OnesCount32(leafCount))
	peakPositions := getPeakPositions(leafCount)

	for _, pos := range peakPositions {
		if pos < len(nodes) {
			peaks = append(peaks, nodes[pos])
		}
	}

	return peaks
}

// getPeakPositions returns the positions of peaks for the given leaf count.
func getPeakPositions(leafCount uint32) []int {
	if leafCount == 0 {
		return nil
	}

	positions := make([]int, 0, bits.OnesCount32(leafCount))
	pos := 0
	remaining := leafCount

	for remaining > 0 {
		height := bits.Len32(remaining) - 1
		treeLeaves := uint32(1) << height
		treeSize := mmrSizeForLeaves(treeLeaves)
		peakPos := pos + treeSize - 1
		positions = append(positions, peakPos)
		pos += treeSize
		remaining -= treeLeaves
	}

	return positions
}

// leafPositionInMMR converts a 0-based leaf index to its position in the MMR array.
func leafPositionInMMR(leafIndex uint32) int {
	if leafIndex == 0 {
		return 0
	}
	return mmrSizeForLeaves(leafIndex)
}

// generateProof generates a merkle proof for leafIndex at the given snapshot count.
func generateProof(nodes [][]byte, leafIndex uint32, snapshotCount uint32) ([][]byte, error) {
	if snapshotCount == 0 || leafIndex >= snapshotCount {
		return nil, ErrLeafIndexOutOfRange
	}

	maxNodes := mmrSizeForLeaves(snapshotCount)
	if maxNodes > len(nodes) {
		return nil, ErrLeafIndexOutOfRange
	}

	proof := make([][]byte, 0, 32)

	// Find which mountain contains this leaf and collect siblings
	mountainStart := 0
	remaining := snapshotCount
	localLeafIdx := leafIndex

	for remaining > 0 {
		height := bits.Len32(remaining) - 1
		mountainLeaves := uint32(1) << height
		mountainSize := mmrSizeForLeaves(mountainLeaves)

		if localLeafIdx < mountainLeaves {
			// Leaf is in this mountain - collect path siblings
			siblings := collectSiblingsInMountain(nodes, mountainStart, height, localLeafIdx)
			proof = append(proof, siblings...)
			break
		}

		mountainStart += mountainSize
		localLeafIdx -= mountainLeaves
		remaining -= mountainLeaves
	}

	// Add other peak hashes for bagging
	peakPositions := getPeakPositions(snapshotCount)
	leafPeakPos := peakPositionForLeaf(leafIndex, snapshotCount)

	for _, pos := range peakPositions {
		if pos != leafPeakPos && pos < maxNodes {
			proof = append(proof, nodes[pos])
		}
	}

	return proof, nil
}

// collectSiblingsInMountain collects sibling hashes from leaf to peak within a perfect subtree.
func collectSiblingsInMountain(nodes [][]byte, mountainStart int, mountainHeight int, localLeafIdx uint32) [][]byte {
	siblings := make([][]byte, 0, mountainHeight)

	// In a perfect binary tree stored in MMR layout:
	// Navigate from leaf to root, collecting siblings at each level

	// Current position starts at the leaf
	pos := mountainStart + leafOffsetInMountain(mountainHeight, localLeafIdx)
	idx := localLeafIdx

	for level := 0; level < mountainHeight; level++ {
		// Determine if current node is left (idx even) or right (idx odd) child
		isLeft := (idx & 1) == 0

		// Calculate sibling position
		var siblingPos int
		if isLeft {
			// Sibling is to the right: next leaf position at this level
			siblingPos = pos + leafSpacing(level)
		} else {
			// Sibling is to the left
			siblingPos = pos - leafSpacing(level)
		}

		if siblingPos >= 0 && siblingPos < len(nodes) {
			siblings = append(siblings, nodes[siblingPos])
		}

		// Move to parent: position is after the rightmost of the pair + 1 for internal node
		if isLeft {
			pos = siblingPos + 1
		} else {
			pos = pos + 1
		}

		// Move to parent index
		idx = idx >> 1
	}

	return siblings
}

// leafOffsetInMountain returns the offset of a leaf within a perfect subtree in MMR layout.
func leafOffsetInMountain(mountainHeight int, localLeafIdx uint32) int {
	// In MMR layout, each leaf at index i within a perfect tree is at position:
	// 2*i - popcount(i)
	// But relative to the mountain start
	return mmrSizeForLeaves(localLeafIdx)
}

// leafSpacing returns the distance to sibling at a given level.
// At level 0 (leaves), spacing is 1.
// At level 1, spacing is 3 (size of subtree at level 0 + 1).
// At level l, spacing is 2^(l+1) - 1.
func leafSpacing(level int) int {
	return (1 << (level + 1)) - 1
}

// peakPositionForLeaf returns the peak position for a given leaf.
func peakPositionForLeaf(leafIndex, snapshotCount uint32) int {
	pos := 0
	remaining := snapshotCount
	targetLeaf := leafIndex

	for remaining > 0 {
		height := bits.Len32(remaining) - 1
		mountainLeaves := uint32(1) << height
		mountainSize := mmrSizeForLeaves(mountainLeaves)

		if targetLeaf < mountainLeaves {
			return pos + mountainSize - 1
		}

		pos += mountainSize
		targetLeaf -= mountainLeaves
		remaining -= mountainLeaves
	}

	return pos
}

// VerifyProof verifies a merkle proof for a leaf.
func VerifyProof(rootHash []byte, snapshotCount uint32, leafIndex uint32, leafData []byte, proof [][]byte) bool {
	if leafIndex >= snapshotCount {
		return false
	}

	peakPositions := getPeakPositions(snapshotCount)
	numPeaks := len(peakPositions)

	// Calculate path length (height of the mountain containing this leaf)
	pathLen := mountainHeightForLeaf(leafIndex, snapshotCount)

	if len(proof) < pathLen {
		return false
	}

	// Compute hash from leaf to peak using path siblings
	currentHash := hashLeaf(leafData)

	// Navigate the path using the same logic as proof generation
	remaining := snapshotCount
	localLeafIdx := leafIndex
	for remaining > 0 {
		height := bits.Len32(remaining) - 1
		mountainLeaves := uint32(1) << height

		if localLeafIdx < mountainLeaves {
			// Found the mountain - traverse it
			idx := localLeafIdx
			for level := 0; level < height; level++ {
				if level >= len(proof) {
					return false
				}
				isLeft := (idx & 1) == 0
				if isLeft {
					currentHash = hashNode(currentHash, proof[level])
				} else {
					currentHash = hashNode(proof[level], currentHash)
				}
				idx = idx >> 1
			}
			break
		}

		localLeafIdx -= mountainLeaves
		remaining -= mountainLeaves
	}

	// Now currentHash is the peak hash for this leaf's mountain
	// Remaining proof elements are other peaks (for multi-peak trees)
	otherPeaks := proof[pathLen:]

	// Build all peaks in order for bagging
	allPeaks := make([][]byte, 0, numPeaks)
	leafPeakIdx := peakIndexForLeaf(leafIndex, snapshotCount)
	otherIdx := 0

	for i := 0; i < numPeaks; i++ {
		if i == leafPeakIdx {
			allPeaks = append(allPeaks, currentHash)
		} else {
			if otherIdx >= len(otherPeaks) {
				return false
			}
			allPeaks = append(allPeaks, otherPeaks[otherIdx])
			otherIdx++
		}
	}

	// Bag peaks from right to left
	computed := allPeaks[len(allPeaks)-1]
	for i := len(allPeaks) - 2; i >= 0; i-- {
		computed = hashNode(allPeaks[i], computed)
	}

	return bytes.Equal(computed, rootHash)
}

// mountainHeightForLeaf returns the height of the mountain containing the leaf.
func mountainHeightForLeaf(leafIndex, snapshotCount uint32) int {
	remaining := snapshotCount
	targetLeaf := leafIndex

	for remaining > 0 {
		height := bits.Len32(remaining) - 1
		mountainLeaves := uint32(1) << height

		if targetLeaf < mountainLeaves {
			return height
		}

		targetLeaf -= mountainLeaves
		remaining -= mountainLeaves
	}

	return 0
}

// peakIndexForLeaf returns which peak (0-based index) contains the leaf.
func peakIndexForLeaf(leafIndex, snapshotCount uint32) int {
	remaining := snapshotCount
	targetLeaf := leafIndex
	peakIdx := 0

	for remaining > 0 {
		height := bits.Len32(remaining) - 1
		mountainLeaves := uint32(1) << height

		if targetLeaf < mountainLeaves {
			return peakIdx
		}

		targetLeaf -= mountainLeaves
		remaining -= mountainLeaves
		peakIdx++
	}

	return peakIdx
}
