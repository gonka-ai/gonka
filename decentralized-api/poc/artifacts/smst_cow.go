package artifacts

import (
	"os"
	"strconv"
	"strings"
)

// SMST_COW / SMST_DEFERRED_HASH / SMST_SNAPSHOT_IN_MEMORY_CLONE /
// SMST_PARALLEL_HASH default on when unset.
// Profiling overrides:
//
//	SMST_COW=0             — in-place Insert (no path-copy)
//	SMST_DEFERRED_HASH=0   — hash on every insert (upgrade-v0.2.14 baseline)
//	SMST_SNAPSHOT_IN_MEMORY_CLONE=0  — tip Prebuild rebuilds from artifacts without holding
//	                         the write lock (upgrade-v0.2.14 Warm/Prebuild path);
//	                         default 1 = deep in-memory clone under write lock
//	SMST_PARALLEL_HASH=0   — serial ensureHashed; default 1 = multicore fill
const envSMSTCOW = "SMST_COW"
const envSMSTDeferredHash = "SMST_DEFERRED_HASH"
const envSMSTSnapshotInMemoryClone = "SMST_SNAPSHOT_IN_MEMORY_CLONE"
const envSMSTParallelHash = "SMST_PARALLEL_HASH"

func smstEnvBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "0", "false", "off", "no":
		return false
	case "1", "true", "on", "yes":
		return true
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// smstCOWEnabledFromEnv reports COW inserts; default true.
func smstCOWEnabledFromEnv() bool {
	return smstEnvBool(envSMSTCOW, true)
}

// smstDeferredHashFromEnv reports deferred Merkle hashing; default true.
func smstDeferredHashFromEnv() bool {
	return smstEnvBool(envSMSTDeferredHash, true)
}

// smstSnapshotInMemoryCloneFromEnv reports tip-snapshot strategy when COW is off:
// true = deep clone under write lock; false = artifact rebuild without write lock.
// Default true. Ignored when COW retains O(1) at flush.
func smstSnapshotInMemoryCloneFromEnv() bool {
	return smstEnvBool(envSMSTSnapshotInMemoryClone, true)
}

// smstParallelHashFromEnv reports multicore ensureHashed; default true.
func smstParallelHashFromEnv() bool {
	return smstEnvBool(envSMSTParallelHash, true)
}

// smstSnapshot is an immutable capture of the tree at a specific leaf count.
// insertCOW never mutates existing nodes, so a captured root stays valid for the
// life of the tree and serves proofs without any rebuild. It is captured after a
// GetRoot (flush/recover), so its nodes are already hashed.
type smstSnapshot struct {
	root  *smstNode
	depth int
	count uint32
}

// insertCOW is a copy-on-write Insert: it rewrites only the nodes on the
// root->leaf path and shares every untouched sibling subtree, so prior roots are
// never clobbered — which is what makes snapshots free. Roots and counts are
// byte-identical to Insert; hashing is deferred identically (filled by
// ensureHashed at GetRoot).
func (s *SMST) insertCOW(nonce int32, leafHash []byte) (uint32, error) {
	if s.hasNonce[nonce] {
		return 0, ErrDuplicateNonce
	}

	requiredDepth := s.requiredDepth(nonce)
	if requiredDepth > s.depth {
		s.expandDepth(requiredDepth)
	}

	path := s.noncePath(nonce)
	s.root = s.insertAtCOW(s.root, path, 0, leafHash)

	s.hasNonce[nonce] = true
	s.leafCount++

	return s.leafCount, nil
}

func (s *SMST) insertAtCOW(node *smstNode, path []bool, level int, leafHash []byte) *smstNode {
	if level == s.depth {
		return &smstNode{hash: leafHash, count: 1}
	}

	newNode := &smstNode{}
	if node != nil {
		newNode.left = node.left
		newNode.right = node.right
	}

	if path[level] {
		newNode.right = s.insertAtCOW(newNode.right, path, level+1, leafHash)
	} else {
		newNode.left = s.insertAtCOW(newNode.left, path, level+1, leafHash)
	}

	newNode.count = s.nodeCount(newNode.left) + s.nodeCount(newNode.right)
	if s.deferredHash {
		newNode.hash = nil // deferred; filled by ensureHashed, identical to insertAt
	} else {
		newNode.hash = s.computeHash(newNode, level)
	}

	return newNode
}

// snapshot captures the current tree. O(1): it retains the root pointer and the
// depth in force at this count (the depth a historical proof must use). Callers
// capture after GetRoot, so the retained nodes are already hashed.
func (s *SMST) snapshot() smstSnapshot {
	return smstSnapshot{root: s.root, depth: s.depth, count: s.leafCount}
}

// cloneSnapshot deep-copies the live tree into an independent snapshot. Used when
// COW is disabled so later in-place inserts cannot mutate historical nodes.
// Callers must hold the store write lock and have already ensureHashed.
func (s *SMST) cloneSnapshot() smstSnapshot {
	return smstSnapshot{root: cloneSMSTNode(s.root), depth: s.depth, count: s.leafCount}
}

func cloneSMSTNode(n *smstNode) *smstNode {
	if n == nil {
		return nil
	}
	out := &smstNode{count: n.count}
	if n.hash != nil {
		out.hash = append([]byte(nil), n.hash...)
	}
	out.left = cloneSMSTNode(n.left)
	out.right = cloneSMSTNode(n.right)
	return out
}

// snapshotView returns a read-only SMST bound to a captured snapshot so proof
// serving uses the depth that was in force at that count, not the live depth.
// emptyHash indices 0..depth are stable across expansion (append-only), so the
// live table is safe to share. Existence is read from the retained structure
// (navExistence) since a snapshot carries no per-count nonce map.
func (s *SMST) snapshotView(snap smstSnapshot) *SMST {
	return &SMST{
		root:         snap.root,
		depth:        snap.depth,
		emptyHash:    s.emptyHash,
		leafCount:    snap.count,
		navExistence: true,
	}
}
