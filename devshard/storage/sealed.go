package storage

// sealedInferenceInsertChunk bounds rows per write transaction. Postgres
// statement_timeout is 5s; one Exec per id would also be a restart-time
// round-trip storm. Chunked transactions keep each commit inside the timeout
// without going back to one RTT per inference.
const sealedInferenceInsertChunk = 500

// sealedInferenceCopyChunk bounds rows per COPY on the post-wipe load path.
// COPY does no per-row conflict probe, so it can carry far more rows per
// statement than the upsert; the chunk exists only to keep one call inside
// statement_timeout and postgresOpTimeout.
const sealedInferenceCopyChunk = 50_000
