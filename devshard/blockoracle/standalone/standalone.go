// Package standalone is the entrypoint helper used by the testenv
// height-sync container: it wires observer.NewMock with server.Mount and
// runs an HTTP listener.
//
// It is kept as a library (not a main package) so cmd/heightsyncd stays a
// trivial shim, and so future callers (e.g. a prod-side standalone
// oracle) can reuse the same wiring.
package standalone

// TODO(phase-3): Run(ctx, Config) error — reads config, constructs
// observer + oracle + server, blocks until ctx is cancelled.
// See devshard/docs/testenv.md §3.5 and Phase 3.
