# Proposal: High-Throughput Incremental Storage for PoC Artifacts

## 1. Definitions

| Term | Meaning |
|------|---------|
| `leaf_index` | Zero-based sequential position of an artifact in the append-only MMR (`uint32`: 0, 1, 2, …). This is the **primary key** for proofs. |
| `nonce_value` | The arbitrary `int32` field from `PoCArtifactV2.nonce`. May be sparse; **must be unique** within a participant's store. |
| `count` | Total number of **leaves** (artifacts) in the MMR (`uint32`). A commit publishes `(root_hash, count)`. |
| `root_hash` | The 32-byte hash obtained by *bagging* MMR peaks (see §5). |
| `snapshot` | A historical state defined by `count`. Proofs are generated against a snapshot. |

---

## 2. Context & Objectives

The system requires a storage engine capable of ingesting high-velocity data streams (**~200,000 artifacts/minute**, ~3.3k/sec) while supporting periodic commitments to the blockchain.

**Key Requirements:**

1. **Throughput:** Handle continuous appending of variable-length artifacts without blocking.
2. **Versioning:** Support verification against *past* states. A validator must be able to verify an artifact against a `root_hash` committed earlier, even if the tree has grown since then.
3. **Efficiency:** Utilize RAM for buffering to minimize disk I/O overhead, flushing only when necessary (e.g., prior to chain commits).
4. **Uniqueness:** Reject duplicate `nonce_value` within a single participant's store.

---

## 3. Proposed Architecture: Memory-Buffered Merkle Mountain Range (MMR)

We use an **append-only Merkle Mountain Range (MMR)**. Unlike balanced trees, MMRs allow calculating the root hash for *any historical count* without data duplication.

### 3.1 Storage Layout (The "4-File" System)

To ensure long-term persistence and random access for validators, we maintain four synchronized files on disk.

**Lifecycle:** Each PoC stage (1-5 minutes generation + validation) uses fresh files. Files are discarded after the stage completes. This short lifecycle simplifies recovery (restart fresh) and bounds resource usage.

#### 1. `artifacts.data` (Payloads)

* Stores raw `PoCArtifactV2` protobuf messages (variable length).
* **Format:** `[Length (4B little-endian)][Protobuf Bytes...]` sequence.

#### 2. `artifacts.index` (Leaf Index → Byte Offset)

* Maps **`leaf_index`** → `byte_offset` in `artifacts.data`.
* **Format:** Fixed-width `uint64` (8 bytes little-endian). Entry `k` at byte `k*8` points to the start of artifact with `leaf_index = k`.
* Provides **O(1)** lookup by `leaf_index`.
* Note: Byte offset is `uint64` to support large data files (>4GB); `leaf_index` itself is `uint32`.

#### 3. `artifacts.tree` (Merkle Structure)

* Stores the 32-byte hashes of every node (leaves and internal nodes) in the MMR.
* **Format:** Fixed `[32 bytes]` sequence.
* **Why:** Allows regenerating proofs for *any* historical snapshot without re-hashing the raw data.

#### 4. `nonces.log` (Nonce Uniqueness Tracking)

* Append-only log of `(nonce_value, leaf_index)` pairs for restart recovery and post-mortems.
* **Format:** `[nonce_value (4B little-endian int32)][leaf_index (4B little-endian uint32)]` sequence.
* At runtime, an in-memory map `map[int32]uint32` (`nonce_value → leaf_index`) is populated on startup by scanning this log. Duplicates are rejected at ingestion time.

---

### 3.2 Memory Strategy (Aggressive Buffering)

Since RAM is available (GBs allowed), we do **not** flush to disk on every write.

* **Write Path:** Incoming artifacts are appended to a large in-memory buffer (e.g., `[]PoCArtifactV2` or raw bytes).
* **Read Path:** Recent items (in buffer) are served from RAM. Older items (flushed) are served from disk.
* **Flush Trigger:** Data is flushed to the 4-file storage only when:
  1. Explicit call of `Flush()` (e.g., before commit)
  2. 5-second timer fires
  3. The buffer reaches a safety threshold (e.g., 1 MB)

---

## 4. Workflow Description

### 4.1 Ingestion Loop

1. **Receive:** `PoCArtifactV2` arrives.
2. **Uniqueness Check:** If `nonce_value` already exists in `nonceToLeafIndex` map → **reject** (error or silent skip, TBD).
3. **Buffer:** Append artifact to the "Hot Buffer" in RAM.
4. **Record Mapping:** `nonceToLeafIndex[nonce_value] = nextLeafIndex`, then `nextLeafIndex++`.
5. **Tree Update:** Calculate the leaf hash and update the in-memory MMR peaks immediately.

### 4.2 Commit (Every ~5 Seconds or Per-Block)

1. **Lock & Flush:**
   * Pause new writes briefly (microseconds).
   * Bulk-write the "Hot Buffer" to `artifacts.data` and `artifacts.index`.
   * Append new tree nodes to `artifacts.tree`.
   * Append new `(nonce_value, leaf_index)` pairs to `nonces.log`.
   * Clear the Hot Buffer.

2. **Calculate Root:**
   * Compute the Merkle Root from the current MMR peaks (see §5).
   * Capture current `count = nextLeafIndex`.

3. **Transaction:**
   * Broadcast `PoCV2StoreCommit(participant_address, poc_stage_start_block_height, root_hash, count)`.
   * *Chain Logic:* The chain accepts this only if `count > last_recorded.count`.

### 4.3 Verification (Versioning Logic)

A validator requests proof for specific `leaf_index` values against the committed `(root_hash, count)`.

**Request:** `{participant_address, poc_stage_start_block_height, root_hash, count, leaf_indices[]}`

**Response per leaf:**
```
{
  leaf_index: uint32,
  nonce_value: int32,
  vector_bytes: bytes,
  proof: [][]byte           // sibling hashes from leaf to peak
}
```

Note: Sibling direction (left vs. right) is derived from MMR position math, not included in the response.

**Verification steps:**

1. Reconstruct `leaf_bytes = encode(nonce_value, vector_bytes)`.
2. Compute `current_hash = hash_leaf(leaf_bytes)`.
3. Derive sibling direction from `leaf_index` and MMR position math.
4. For each sibling in proof, hash with `current_hash` in the correct order.
5. The result is the peak hash for the mountain containing this leaf.
6. Bag all peaks (using the committed `count` to identify peak positions) and compare to `root_hash`.
7. Check sampled `nonce_value` uniqueness within response set; duplicates ⇒ participant invalid.

---

## 5. Normative MMR Specification

This section is **normative**. Implementations MUST follow these rules for interoperability.

### 5.1 Hash Function

* **Algorithm:** SHA-256 (32 bytes output).

### 5.2 Domain Separation

* **Leaf prefix:** `0x00`
* **Internal node prefix:** `0x01`

```
hash_leaf(data)       = SHA256(0x00 || data)
hash_node(left,right) = SHA256(0x01 || left || right)
```

### 5.3 Leaf Preimage Encoding

The leaf data is the canonical encoding of the artifact:

```
leaf_bytes = LE32(nonce_value) || vector_bytes
```

Where `LE32` is little-endian 4-byte encoding. For signed `int32`, this preserves the bit pattern directly (e.g., -1 encodes as `0xFFFFFFFF`). Implementations must use bitwise conversion, not mathematical conversion.

### 5.4 MMR Structure

An MMR is an append-only forest of perfect binary trees ("mountains"). When adding a new leaf:

1. Append the leaf hash at the next position.
2. While the new node completes a pair (i.e., forms a right child), merge with its left sibling to create a parent node and append that parent.

**Position Indexing:** We use 1-based positions for internal MMR node storage (standard in Grin/Mimblewimble). The mapping from 0-based `leaf_index` to 1-based MMR position is:

```
mmr_position(leaf_index) = leaf_index * 2 + 1 - popcount(leaf_index)
```

(Implementation note: this formula accounts for internal nodes inserted before the leaf.)

### 5.5 Peak Identification

For a given `count` (number of leaves), the peaks are the roots of the perfect binary trees in the MMR forest. Peak positions can be derived from the binary representation of `count`.

### 5.6 Peak Bagging (Root Calculation)

The `root_hash` is computed by **bagging peaks from right to left**:

```
if len(peaks) == 0:
    return nil
    
root = peaks[len(peaks)-1]  // rightmost peak
for i = len(peaks)-2 down to 0:
    root = hash_node(peaks[i], root)
return root
```

### 5.7 Snapshot Semantics

* `count` is the number of **leaves**, not the number of MMR nodes.
* A proof for `leaf_index` against snapshot `count` must only reference nodes that existed when the tree had exactly `count` leaves.
* The verifier reconstructs peak positions from `count` and bags them to get `root_hash`.

---

## 6. Estimations

**Throughput:**

* **200,000 items/minute** = ~3,333 items/sec.
* **Disk I/O:** With 5-second flushes, we perform 1 large sequential write every 5s instead of 16,000 small writes. This is negligible load.

**Storage Footprint (Disk):**

Assuming ~100 bytes per artifact:

| Duration | Artifact Count | Disk Usage (Data + Index + Tree + Nonces) |
|----------|----------------|-------------------------------------------|
| **1 Minute** | 200,000 | ~40 MB |
| **1 Hour** | 12,000,000 | ~2.4 GB |
| **24 Hours** | 288,000,000 | ~58 GB |

**RAM Usage:**

* Buffering 5 seconds of data: ~3 MB (trivial).
* Buffering 1 minute of data: ~40 MB (trivial).
* In-memory `nonce_value → leaf_index` map: ~16 bytes per entry × 12M entries/hour ≈ 192 MB/hour.
* *Conclusion:* We can comfortably buffer large chunks.

**Network Payload (Validation):**

Requesting **100 random leaf indices** (using `PocParams.ValidationSampleSize`):

* **Proof:** ~60 KB (100 proofs × ~600 bytes each).
* **Data:** ~10 KB.
* **Total:** < 75 KB per request.

---

## 7. Implementation Notes (Non-Normative Pseudocode)

> **Warning:** The code below is illustrative pseudocode, not production-ready. It omits error handling, concurrency, and optimizations. Refer to §5 for the normative specification.

### 7.1 Protobuf Definition

```protobuf
message PoCArtifactV2 {
  int32 nonce = 1;   // changed from int64
  bytes vector = 2; 
}
```

### 7.2 Storage Engine Sketch

```go
type ArtifactStore struct {
    mu sync.RWMutex

    // Files (Append-Only)
    dataFile   *os.File
    idxFile    *os.File
    treeFile   *os.File
    noncesFile *os.File

    // In-Memory State
    buffer           []*PoCArtifactV2
    nonceToLeafIndex map[int32]uint32  // Uniqueness tracking
    nextLeafIndex    uint32
    mmrPeaks         [][]byte          // Current MMR peak hashes
    mmrNodeCount     uint32            // Total nodes in tree file
}

// Add appends to RAM buffer after uniqueness check.
func (s *ArtifactStore) Add(art *PoCArtifactV2) error {
    s.mu.Lock()
    defer s.mu.Unlock()

    if _, exists := s.nonceToLeafIndex[art.Nonce]; exists {
        return ErrDuplicateNonce
    }

    s.nonceToLeafIndex[art.Nonce] = s.nextLeafIndex
    s.buffer = append(s.buffer, art)
    
    // Update MMR peaks in memory
    leafBytes := encodeLeaf(art)
    leafHash := hashLeaf(leafBytes)
    s.appendToMMR(leafHash)
    
    s.nextLeafIndex++
    return nil
}

// GetProof returns artifact data and proof for leaf_index at snapshot count.
func (s *ArtifactStore) GetProof(leafIndex uint32, snapshotCount uint32) (
    artifact *PoCArtifactV2, 
    proof [][]byte, 
    err error,
) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    
    if leafIndex >= snapshotCount {
        return nil, nil, ErrLeafIndexOutOfRange
    }
    
    // Fetch artifact by leaf_index
    artifact = s.getArtifactByLeafIndex(leafIndex)
    
    // Generate proof at snapshot (direction derived from position math)
    proof = s.generateMMRProof(leafIndex, snapshotCount)
    
    return artifact, proof, nil
}
```

### 7.3 Hashing Helpers

```go
func hashLeaf(data []byte) []byte {
    h := sha256.New()
    h.Write([]byte{0x00})  // Leaf prefix
    h.Write(data)
    return h.Sum(nil)
}

func hashNode(left, right []byte) []byte {
    h := sha256.New()
    h.Write([]byte{0x01})  // Internal node prefix
    h.Write(left)
    h.Write(right)
    return h.Sum(nil)
}

func encodeLeaf(art *PoCArtifactV2) []byte {
    buf := make([]byte, 4+len(art.Vector))
    binary.LittleEndian.PutUint32(buf[:4], uint32(art.Nonce))
    copy(buf[4:], art.Vector)
    return buf
}

func bagPeaks(peakHashes [][]byte) []byte {
    if len(peakHashes) == 0 {
        return nil
    }
    // Bag from right to left
    root := peakHashes[len(peakHashes)-1]
    for i := len(peakHashes) - 2; i >= 0; i-- {
        root = hashNode(peakHashes[i], root)
    }
    return root
}
```

### 7.4 Client-Side Verification

```go
func Verify(
    rootHash []byte,
    snapshotCount uint32,
    leafIndex uint32,
    nonceValue int32,
    vectorBytes []byte,
    proof [][]byte,
) bool {
    // 1. Reconstruct leaf hash
    leafBytes := make([]byte, 4+len(vectorBytes))
    binary.LittleEndian.PutUint32(leafBytes[:4], uint32(nonceValue))
    copy(leafBytes[4:], vectorBytes)
    currentHash := hashLeaf(leafBytes)

    // 2. Climb to peak
    // Direction (left vs right sibling) is derived from MMR position math
    pos := mmrPositionFromLeafIndex(leafIndex)
    for _, sibling := range proof {
        isRightSibling := isCurrentNodeLeftChild(pos, snapshotCount)
        if isRightSibling {
            currentHash = hashNode(currentHash, sibling)
        } else {
            currentHash = hashNode(sibling, currentHash)
        }
        pos = parentPosition(pos)
    }

    // 3. Bag peaks (remaining proof items are other peaks)
    // The verifier identifies peaks from snapshotCount and combines
    
    // 4. Compare
    return bytes.Equal(currentHash, rootHash)
}
```

---

## 8. Threat Model & Invariants

### Invariants

1. **Uniqueness:** Within one participant's store, `nonce_value` is unique. Verified by in-memory map; rejecting duplicates at ingestion.
2. **Append-only:** Once committed, a `(root_hash, count)` pair is immutable. The chain only accepts commits with strictly increasing `count`.
3. **Determinism:** Given the same sequence of artifacts (in append order), any implementation produces the same `root_hash`.

### What is Trusted vs. Untrusted

| Aspect | Trust Model |
|--------|-------------|
| `count` and `root_hash` | Committed on-chain; trusted once recorded. |
| Artifact data | Participant-controlled; proofs bind data to the commitment. |
| `nonce_value` uniqueness | Verified by validators via sampling; duplicates invalidate participant. |

---

## 9. References (Current Implementation)

These code paths illustrate the *current* PoC v2 implementation (on-chain artifacts). The off-chain approach replaces batch storage with commit-only.

* **PoC v2 batch submission & window gating:**
  - `inference-chain/x/inference/keeper/msg_server_submit_poc_v2.go` — `SubmitPocBatchesV2` validates epoch windows via `CheckPoCMessageTooLate`.

* **Validation sample size parameter:**
  - `decentralized-api/internal/pocv2/node_orchestrator.go` — uses `pocParams.ValidationSampleSize` (lines 253-264).

* **Current on-chain comment acknowledging future off-chain:**
  - `inference-chain/proto/inference/inference/tx.proto` — `"Note: Current iteration stores artifacts on-chain; later iteration moves fully off-chain."`
