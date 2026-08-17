# Phase 1: MMR Benchmark Results

Baseline performance measurements for the current MMR-based artifact store.

## Test Environment

- CPU: AMD Ryzen 9 9950X 16-Core Processor
- OS: Linux
- Go: standard crypto/sha256

## Write Throughput

| Operation | Throughput | Latency |
|:----------|:-----------|:--------|
| Add (in-memory) | ~2M ops/sec | 504 ns/op |
| Add (with flush every 1K) | ~330K ops/sec | 3012 ns/op |

MMR append is O(1) amortized - each insert adds a leaf and merges sibling nodes based on trailing zeros in the leaf count.

## Read Throughput

| Operation | Tree Size | Throughput | Latency |
|:----------|:----------|:-----------|:--------|
| GetProof | 100K | ~4.8M ops/sec | 254 ns/op |
| VerifyProof | 100K | ~510K ops/sec | 1959 ns/op |

Proof generation is fast because the MMR nodes are in memory. Verification is slower due to hash computation.

## Proof Size

| Tree Size | Avg Proof Size | Hash Count |
|:----------|:---------------|:-----------|
| 1,000 | 420 bytes | 13 |
| 10,000 | 523 bytes | 16 |
| 100,000 | 659 bytes | 20 |
| 1,000,000 | 775 bytes | 24 |
| 5,000,000 | 911 bytes | 28 |

Proof size grows logarithmically. At 5M artifacts, average proof is ~900 bytes (28 SHA-256 hashes).

## Recovery Time (Disk Load)

| Artifact Count | Recovery Time | Rate |
|:---------------|:--------------|:-----|
| 10,000 | 6.5 ms | 1.5M artifacts/sec |
| 100,000 | 68 ms | 1.5M artifacts/sec |
| 1,000,000 | 742 ms | 1.3M artifacts/sec |

Recovery reads artifacts.data sequentially and rebuilds the MMR in memory. At 5M artifacts (production scale), estimated recovery time is ~3.5 seconds.

## Analysis

The MMR implementation is well-optimized:

1. In-memory inserts are fast (500ns) due to O(1) append
2. Proof generation is memory-bound, not CPU-bound
3. Recovery scales linearly with artifact count
4. Proof sizes are reasonable but grow with tree size

For SMST comparison (Phase 4), key metrics to track:
- Insert latency (expected: higher due to O(D) vs O(1))
- Proof size (expected: smaller, fixed depth)
- Recovery time (expected: similar, same data format)
