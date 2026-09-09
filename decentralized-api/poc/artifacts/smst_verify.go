package artifacts

import "encoding/binary"

func smstNoncePath(nonce int32, depth int) []bool {
	path := make([]bool, depth)
	n := uint32(nonce)
	for i := 0; i < depth; i++ {
		bit := (n >> (depth - 1 - i)) & 1
		path[i] = bit == 1
	}
	return path
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func verifySMSTProofWithCounts(rootHash []byte, count uint32, nonce int32, leafData []byte, proof []smstProofElement) bool {
	if len(proof) == 0 {
		return false
	}

	depth := len(proof)
	leafHash := smstHashLeaf(leafData)
	path := smstNoncePath(nonce, depth)

	currentHash := leafHash
	currentCount := uint32(1)

	for i := depth - 1; i >= 0; i-- {
		elem := proof[i]
		goRight := path[i]

		var leftHash, rightHash []byte
		var leftCount, rightCount uint32

		if goRight {
			leftHash = elem.siblingHash
			rightHash = currentHash
			leftCount = elem.siblingCount
			rightCount = currentCount
		} else {
			leftHash = currentHash
			rightHash = elem.siblingHash
			leftCount = currentCount
			rightCount = elem.siblingCount
		}

		currentCount = leftCount + rightCount
		currentHash = smstHashNode(leftHash, rightHash, currentCount)
	}

	if currentCount != count {
		return false
	}

	return bytesEqual(currentHash, rootHash)
}

type smstProofElement struct {
	siblingHash  []byte
	siblingCount uint32
}

// decodeProofElements decodes proof from slice format ([][]byte where each is 36 bytes).
func decodeProofElements(proof [][]byte) []smstProofElement {
	elements := make([]smstProofElement, len(proof))
	for i, data := range proof {
		if len(data) != 36 {
			return nil
		}
		elements[i].siblingHash = data[:32]
		elements[i].siblingCount = binary.LittleEndian.Uint32(data[32:36])
	}
	return elements
}


// VerifySMSTProofWithDenseIndex verifies an SMST proof and checks that the nonce
// is at the claimed dense index position. The sibling counts in the proof are
// committed by the root hash, making the index binding cryptographically sound.
//
// Security: This function is hardened against overflow attacks. The computedIndex
// accumulator uses uint64 to detect overflow, and we explicitly check that it
// stays within bounds of count. Since sibling counts are committed by the root hash,
// a malicious prover cannot forge counts without changing the root.
func VerifySMSTProofWithDenseIndex(rootHash []byte, count uint32, denseIndex uint32, nonce int32, leafData []byte, proof [][]byte) bool {
	// Reject edge cases: empty proof, zero count, or out-of-bounds index
	if len(proof) == 0 || count == 0 || denseIndex >= count {
		return false
	}

	elements := decodeProofElements(proof)
	if elements == nil {
		return false
	}

	if !verifySMSTProofWithCounts(rootHash, count, nonce, leafData, elements) {
		return false
	}

	depth := len(elements)
	path := smstNoncePath(nonce, depth)

	var computedIndex uint64
	for i := 0; i < depth; i++ {
		if path[i] {
			computedIndex += uint64(elements[i].siblingCount)
			if computedIndex >= uint64(count) && computedIndex != uint64(denseIndex) {
				return false
			}
		}
	}

	// Final check: computed index must exactly match claimed dense index
	return computedIndex == uint64(denseIndex)
}
