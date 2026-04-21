// Package engine provides stub implementations of devshard.InferenceEngine
// and devshard.ValidationEngine for the testenv.
//
// These stubs never contact real ML nodes. They produce deterministic,
// hash-seeded output suitable for protocol-level assertions; fault
// injection (latency, error, verdict flips) is controlled via request
// headers and per-host config. See devshard/docs/testenv.md §Phase 7.
package engine

// TODO(phase-7): MockInferenceEngine with NewMockInference(cfg) constructor.
