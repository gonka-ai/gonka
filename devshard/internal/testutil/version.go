package testutil

// RuntimeTestVersion is the versiond runtime / storage bind tag used in tests
// (CreateSessionParams.Version, host boundVersion). It is not the state-root
// or settlement protocol version; use types.DevshardStateRootAndProtocolVersion
// for hash, settlement, and migration tests. See devshard/docs/protocol-version.md.
const RuntimeTestVersion = "v1"
