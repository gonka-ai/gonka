# Phase 4: SMST Benchmark Results

Performance comparison between MMR and SMST artifact stores.

## Test Environment

- CPU: AMD Ryzen 9 9950X 16-Core Processor
- OS: Linux
- Go: standard crypto/sha256

## Write Throughput

| Store | Operation | Throughput | Latency |
|:------|:----------|:-----------|:--------|
| MMR | Add (in-memory) | 2.0M ops/sec | 504 ns/op |
| SMST | Add (in-memory) | 355K ops/sec | 2818 ns/op |
| MMR | Add (with flush) | 330K ops/sec | 3012 ns/op |
| SMST | Add (with flush) | 187K ops/sec | 5349 ns/op |

MMR is ~5.6x faster for in-memory inserts because it's O(1) amortized vs O(24) for SMST tree traversal.

## Read Throughput

| Store | Operation | Throughput | Latency |
|:------|:----------|:-----------|:--------|
| MMR | GetProof | 4.8M ops/sec | 254 ns/op |
| SMST | GetProof | 823K ops/sec | 1215 ns/op |
| MMR | VerifyProof | 510K ops/sec | 1959 ns/op |
| SMST | VerifyProof | 387K ops/sec | 2584 ns/op |

SMST proof generation is slower due to tree traversal vs direct array access in MMR. Verification is comparable since both require ~24 hash computations.

## Proof Size

| Store | Tree Size | Proof Size | Elements |
|:------|:----------|:-----------|:---------|
| MMR | 100K | 659 bytes | 20 hashes |
| SMST | 100K | 864 bytes | 24 (hash+count) |
| MMR | 5M | 911 bytes | 28 hashes |
| SMST | 5M | 864 bytes | 24 (hash+count) |

SMST proofs are fixed at 864 bytes (24 levels x 36 bytes). MMR proofs grow with tree size but are hash-only (32 bytes each). At 5M artifacts SMST is slightly smaller.

## Recovery Time (Disk Load)

| Store | Artifact Count | Recovery Time | Rate |
|:------|:---------------|:--------------|:-----|
| MMR | 10K | 6.5 ms | 1.5M/sec |
| SMST | 10K | 31.5 ms | 317K/sec |
| MMR | 100K | 68 ms | 1.5M/sec |
| SMST | 100K | 312 ms | 320K/sec |
| MMR | 1M | 742 ms | 1.3M/sec |
| SMST | 1M | 3037 ms | 329K/sec |

SMST recovery is ~4x slower because each artifact requires a full tree traversal during insertion. At 5M artifacts, estimated SMST recovery is ~15 seconds.

## Summary

| Metric | MMR | SMST | Trade-off |
|:-------|:----|:-----|:----------|
| Insert | 2M/sec | 355K/sec | 5.6x slower |
| Recovery | 742 ms/1M | 3037 ms/1M | 4x slower |
| Proof size | Variable (grows) | Fixed 864 bytes | SMST wins at scale |
| Duplicate prevention | No | Yes | SMST required |

SMST is slower but provides the essential duplicate prevention property. The performance is acceptable for production use:
- 355K inserts/sec handles burst traffic easily
- 3-second recovery at 1M artifacts is reasonable for restart
- Fixed proof size is actually an advantage at high scale

The trade-off is worth it because duplicate prevention is a security requirement.
