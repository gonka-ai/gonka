package mockdapi

// TODO(phase-5): MockDapi holds the constructed Oracle and NodeManager.
// New(ctx, Config) (*MockDapi, error) wires:
//   - blockoracle/client.NewHTTP for HeightSyncURL
//   - verifier.Verify pinned to ValidatorPubHex + ChainID
//   - no-op NodeManager.
// See devshard/docs/testenv.md §5.
