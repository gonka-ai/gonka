package artifacts

import (
	"crypto/sha256"
	"encoding/binary"
	"runtime"
	"sync"
)

const (
	smstLeafPrefix     = 0x00
	smstInternalPrefix = 0x01
	// smstDefaultDepth is the starting depth for both live trees and snapshot
	// rebuilds, so it participates in root reproducibility: changing it changes
	// committed roots and must only happen at a coordinated protocol upgrade.
	smstDefaultDepth = 24
	smstMaxDepth     = 32
	// smstParallelHashMinCount: only fan out ensureHashed work when both
	// children need hashing and the subtree has at least this many leaves.
	smstParallelHashMinCount = 256
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

	// navExistence makes HasNonce walk the tree instead of the hasNonce map.
	// Snapshot views share nodes with the live tree but carry no map, so nonce
	// existence must be read from the retained structure at that count.
	navExistence bool

	// deferredHash invalidates node.hash on insert and fills via ensureHashed
	// (default). When false, hashes are computed on every insert (upgrade-v0.2.14).
	deferredHash bool

	// parallelHash fans out ensureHashed across GOMAXPROCS when both children
	// need hashing (default on). Eager per-insert path hashing stays serial
	// (parent depends on child); this flag mainly accelerates deferred fill.
	parallelHash bool
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
		depth:         depth,
		emptyHash:     make([][]byte, depth+1),
		hasNonce:      make(map[int32]bool),
		deferredHash:  true,
		parallelHash:  true,
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
	if s.deferredHash {
		node.hash = nil // invalidated; recomputed lazily by ensureHashed
	} else {
		node.hash = s.computeHash(node, level)
	}

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
	s.ensureHashed()
	return s.root.hash, s.root.count
}

// ensureHashed fills node hashes left nil by insertAt. Each node is hashed once
// here rather than on every insert that passes through it, so a shared upper
// node is not re-hashed for each descendant insert. Idempotent: a node whose
// hash is already set stops the recursion. When parallelHash is on, independent
// subtrees are hashed concurrently (bounded by GOMAXPROCS).
func (s *SMST) ensureHashed() {
	if s.root == nil {
		return
	}
	if !s.parallelHash {
		s.hashNode(s.root, 0)
		return
	}
	workers := runtime.GOMAXPROCS(0)
	if workers < 2 {
		s.hashNode(s.root, 0)
		return
	}
	// Main goroutine counts as one worker; sem holds the remaining slots.
	sem := make(chan struct{}, workers-1)
	s.hashNodeParallel(s.root, 0, sem)
}

func (s *SMST) hashNode(node *smstNode, level int) {
	if node == nil || node.hash != nil {
		return
	}
	s.hashNode(node.left, level+1)
	s.hashNode(node.right, level+1)
	node.hash = s.computeHash(node, level)
}

func (s *SMST) hashNodeParallel(node *smstNode, level int, sem chan struct{}) {
	if node == nil || node.hash != nil {
		return
	}
	left, right := node.left, node.right
	canFanOut := left != nil && left.hash == nil &&
		right != nil && right.hash == nil &&
		node.count >= smstParallelHashMinCount
	if canFanOut {
		select {
		case sem <- struct{}{}:
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				s.hashNodeParallel(left, level+1, sem)
			}()
			s.hashNodeParallel(right, level+1, sem)
			wg.Wait()
			node.hash = s.computeHash(node, level)
			return
		default:
			// All worker slots busy; finish this subtree on the current goroutine.
		}
	}
	s.hashNodeParallel(left, level+1, sem)
	s.hashNodeParallel(right, level+1, sem)
	node.hash = s.computeHash(node, level)
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
	if s.navExistence {
		return s.hasNonceInTree(nonce)
	}
	return s.hasNonce[nonce]
}

func (s *SMST) hasNonceInTree(nonce int32) bool {
	if s.root == nil {
		return false
	}
	// A nonce needing more depth than this tree has could never have been
	// inserted without expanding it, so it is absent. Below the depth the path
	// bits are injective, making the walk exact — matching the hasNonce map.
	if s.requiredDepth(nonce) > s.depth {
		return false
	}
	node := s.root
	for _, goRight := range s.noncePath(nonce) {
		if node == nil {
			return false
		}
		if goRight {
			node = node.right
		} else {
			node = node.left
		}
	}
	return node != nil
}

func (s *SMST) denseIndexForNonce(nonce int32) (uint32, error) {
	if s.root == nil || !s.HasNonce(nonce) {
		return 0, ErrNonceNotFound
	}

	path := s.noncePath(nonce)
	node := s.root
	var denseIndex uint32
	for _, goRight := range path {
		if node == nil {
			return 0, ErrNonceNotFound
		}
		if goRight {
			denseIndex += s.nodeCount(node.left)
			node = node.right
		} else {
			node = node.left
		}
	}
	if node == nil {
		return 0, ErrNonceNotFound
	}

	return denseIndex, nil
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
			if s.deferredHash {
				// Wrapper hash is deferred; ensureHashed fills it from the wrapped
				// subtree and the empty sibling, identical to eager computation.
				s.root = &smstNode{
					left:  s.root,
					count: s.root.count,
				}
			} else {
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
