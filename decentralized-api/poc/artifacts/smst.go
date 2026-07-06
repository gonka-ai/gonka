package artifacts

import (
	"crypto/sha256"
	"encoding/binary"
)

const (
	smstLeafPrefix     = 0x00
	smstInternalPrefix = 0x01
	// smstDefaultDepth is the starting depth for both live trees and snapshot
	// rebuilds, so it participates in root reproducibility: changing it changes
	// committed roots and must only happen at a coordinated protocol upgrade.
	smstDefaultDepth = 24
	smstMaxDepth     = 32
)

type smstNode struct {
	hash  []byte
	count uint32
	left  *smstNode
	right *smstNode
}

// SMST is a Sparse Merkle Sum Tree where nonce determines the path.
// Sum property: each node stores count = left.count + right.count.
// Enables dense index navigation in sparse tree.
type SMST struct {
	root      *smstNode
	depth     int
	emptyHash [][]byte
	leafCount uint32
	hasNonce  map[int32]bool // tracks which nonces exist (for duplicate detection)
}

// NewSMST creates a new sparse merkle sum tree.
// Depth determines max nonce: 2^depth - 1. Default is 24 (16.7M nonces).
func NewSMST(depth int) *SMST {
	if depth <= 0 {
		depth = smstDefaultDepth
	}
	if depth > smstMaxDepth {
		depth = smstMaxDepth
	}

	s := &SMST{
		depth:     depth,
		emptyHash: make([][]byte, depth+1),
		hasNonce:  make(map[int32]bool),
	}

	s.emptyHash[0] = smstHashEmpty()
	for i := 1; i <= depth; i++ {
		s.emptyHash[i] = smstHashNode(s.emptyHash[i-1], s.emptyHash[i-1], 0)
	}

	return s
}

// Insert adds a leaf at the position determined by nonce.
// Returns the new leaf count after insertion.
// Returns error if nonce already exists.
func (s *SMST) Insert(nonce int32, leafHash []byte) (uint32, error) {
	if s.hasNonce[nonce] {
		return 0, ErrDuplicateNonce
	}

	requiredDepth := s.requiredDepth(nonce)
	if requiredDepth > s.depth {
		s.expandDepth(requiredDepth)
	}

	path := s.noncePath(nonce)
	s.root = s.insertAt(s.root, path, 0, leafHash)

	s.hasNonce[nonce] = true
	s.leafCount++

	return s.leafCount, nil
}

func (s *SMST) insertAt(node *smstNode, path []bool, level int, leafHash []byte) *smstNode {
	if level == s.depth {
		return &smstNode{
			hash:  leafHash,
			count: 1,
		}
	}

	if node == nil {
		node = &smstNode{}
	}

	goRight := path[level]
	if goRight {
		node.right = s.insertAt(node.right, path, level+1, leafHash)
	} else {
		node.left = s.insertAt(node.left, path, level+1, leafHash)
	}

	node.count = s.nodeCount(node.left) + s.nodeCount(node.right)
	node.hash = s.computeHash(node, level)

	return node
}

func (s *SMST) nodeCount(node *smstNode) uint32 {
	if node == nil {
		return 0
	}
	return node.count
}

func (s *SMST) nodeHash(node *smstNode, level int) []byte {
	if node == nil {
		return s.emptyHash[s.depth-level]
	}
	return node.hash
}

func (s *SMST) computeHash(node *smstNode, level int) []byte {
	leftHash := s.nodeHash(node.left, level+1)
	rightHash := s.nodeHash(node.right, level+1)
	return smstHashNode(leftHash, rightHash, node.count)
}

// GetRoot returns the root hash and total count.
func (s *SMST) GetRoot() ([]byte, uint32) {
	if s.root == nil {
		return s.emptyHash[s.depth], 0
	}
	return s.root.hash, s.root.count
}

// GetLeafByDenseIndex navigates to a leaf using sum-based dense indexing.
// Returns the nonce and proof (sibling hashes from root to leaf).
func (s *SMST) GetLeafByDenseIndex(denseIndex uint32) (int32, [][]byte, error) {
	if s.root == nil || denseIndex >= s.root.count {
		return 0, nil, ErrLeafIndexOutOfRange
	}

	proof := make([][]byte, 0, s.depth)
	path := make([]bool, 0, s.depth)
	err := s.navigateToLeaf(s.root, denseIndex, 0, &proof, &path)
	if err != nil {
		return 0, nil, err
	}

	nonce := s.pathToNonce(path)
	return nonce, proof, nil
}

func (s *SMST) navigateToLeaf(node *smstNode, index uint32, level int, proof *[][]byte, path *[]bool) error {
	if level == s.depth {
		return nil
	}

	if node == nil {
		return ErrLeafIndexOutOfRange
	}

	leftCount := s.nodeCount(node.left)

	if index < leftCount {
		*proof = append(*proof, s.nodeHash(node.right, level+1))
		*path = append(*path, false)
		return s.navigateToLeaf(node.left, index, level+1, proof, path)
	}

	*proof = append(*proof, s.nodeHash(node.left, level+1))
	*path = append(*path, true)
	return s.navigateToLeaf(node.right, index-leftCount, level+1, proof, path)
}

func (s *SMST) pathToNonce(path []bool) int32 {
	var n uint32
	for i := 0; i < len(path); i++ {
		if path[i] {
			n |= 1 << (s.depth - 1 - i)
		}
	}
	return int32(n)
}

// Count returns the number of leaves in the tree.
func (s *SMST) Count() uint32 {
	return s.leafCount
}

// Depth returns the current tree depth.
func (s *SMST) Depth() int {
	return s.depth
}

// HasNonce checks if a nonce exists in the tree.
func (s *SMST) HasNonce(nonce int32) bool {
	return s.hasNonce[nonce]
}

func (s *SMST) noncePath(nonce int32) []bool {
	path := make([]bool, s.depth)
	n := uint32(nonce)
	for i := 0; i < s.depth; i++ {
		bit := (n >> (s.depth - 1 - i)) & 1
		path[i] = bit == 1
	}
	return path
}

func (s *SMST) requiredDepth(nonce int32) int {
	n := uint32(nonce)
	if n == 0 {
		return 1
	}
	bits := 0
	for n > 0 {
		bits++
		n >>= 1
	}
	return bits
}

func (s *SMST) expandDepth(newDepth int) {
	if newDepth > smstMaxDepth {
		newDepth = smstMaxDepth
	}
	if newDepth <= s.depth {
		return
	}

	// Precompute empty hashes for new depths
	for i := s.depth + 1; i <= newDepth; i++ {
		s.emptyHash = append(s.emptyHash, smstHashNode(s.emptyHash[i-1], s.emptyHash[i-1], 0))
	}

	// Update depth first so nodeHash uses correct empty hash indices
	oldDepth := s.depth
	s.depth = newDepth

	// Wrap existing tree: old root becomes left child at each level.
	// Right sibling at each wrapper level is empty with height = (newDepth - level).
	// We wrap from inside out: first wrapper is at level (newDepth - oldDepth - 1),
	// last wrapper is at level 0.
	diff := newDepth - oldDepth
	for i := 0; i < diff; i++ {
		if s.root != nil {
			// This wrapper will be at level (diff - 1 - i) in final tree
			level := diff - 1 - i
			siblingHeight := newDepth - level - 1
			newRoot := &smstNode{
				left:  s.root,
				count: s.root.count,
			}
			newRoot.hash = smstHashNode(s.root.hash, s.emptyHash[siblingHeight], newRoot.count)
			s.root = newRoot
		}
	}
}

func smstHashLeaf(data []byte) []byte {
	h := sha256.New()
	h.Write([]byte{smstLeafPrefix})
	h.Write(data)
	return h.Sum(nil)
}

func smstHashNode(left, right []byte, count uint32) []byte {
	h := sha256.New()
	h.Write([]byte{smstInternalPrefix})
	h.Write(left)
	h.Write(right)
	countBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBytes, count)
	h.Write(countBytes)
	return h.Sum(nil)
}

func smstHashEmpty() []byte {
	h := sha256.New()
	h.Write([]byte{smstLeafPrefix})
	return h.Sum(nil)
}
