package storage

// sealedInferenceInsertChunk bounds rows per write transaction. Postgres
// statement_timeout is 5s; one Exec per id would also be a restart-time
// round-trip storm. Chunked transactions keep each commit inside the timeout
// without going back to one RTT per inference.
const sealedInferenceInsertChunk = 500
