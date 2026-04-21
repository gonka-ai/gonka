package mockdapi

// TODO(phase-5): BlockOracle adapter.
// Wraps blockoracle/client.NewHTTP, runs the SSE subscription, and
// re-verifies each ingested header via blockoracle/verifier before
// making it visible on the blockoracle.BlockOracle interface.
