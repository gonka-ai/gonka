package artifacts

// ArtifactStore defines the interface for PoC artifact storage with Merkle commitments.
// Implementations must be safe for concurrent use.
//
// All read operations that return artifacts or proofs require a snapshot count parameter.
// This is critical for SMST correctness: the dense index to nonce mapping depends on tree state.
// Unlike MMR where leaf positions were stable, SMST dense indices change as leaves are added.
type ArtifactStore interface {
	// AddWithNode appends an artifact and tracks which node contributed it.
	// Returns ErrDuplicateNonce if nonce already exists.
	AddWithNode(nonce int32, vector []byte, nodeId string) error

	// GetRootAt returns the root hash at a specific snapshot count.
	// Returns nil if snapshotCount is 0, error if snapshotCount exceeds current count.
	GetRootAt(snapshotCount uint32) ([]byte, error)

	// GetFlushedRoot returns the root and count of ONLY persisted artifacts.
	// Safe to report externally - survives process crashes.
	GetFlushedRoot() (count uint32, root []byte)

	// GetNodeDistributionAt returns node distribution at a specific count.
	// Returns exact=true if found in history, exact=false if simulated.
	// Simulated distribution is scaled proportionally and sums to count.
	GetNodeDistributionAt(count uint32) (distribution map[string]uint32, exact bool, err error)

	// GetNodeCounts returns current (unflushed) node distribution.
	GetNodeCounts() map[string]uint32

	// Count returns the total number of artifacts (including unflushed).
	Count() uint32

	// GetArtifactAndProof retrieves artifact and proof for a dense index at a specific snapshot.
	// This is the ONLY way to retrieve artifacts - snapshot awareness is mandatory for SMST.
	// Dense index is the sequential position [0, snapshotCount) computed from sibling counts.
	GetArtifactAndProof(denseIndex uint32, snapshotCount uint32) (nonce int32, vector []byte, proof [][]byte, err error)

	// Flush persists buffered artifacts to disk.
	Flush() error

	// Close flushes and releases resources.
	Close() error

	// PrebuildSnapshot builds and caches tree state at specified count for fast proofs.
	PrebuildSnapshot(count uint32) error
}
