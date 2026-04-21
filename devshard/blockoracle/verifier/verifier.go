// Package verifier validates a blockoracle.Header against a pinned chain
// ID and validator set.
//
// The same verifier runs in every runtime (producer + consumer) so
// tampering is caught at ingest regardless of who served the header.
package verifier

// TODO(phase-1): Verify(header, chainID, valset) enforces:
//   - chain ID equality
//   - signature recovery per CommitSig
//   - > 2/3 voting power of the pinned validator set
//   - block-hash / app-hash integrity.
// See devshard/docs/testenv.md §3.1.
