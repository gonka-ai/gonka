// Binary heightsyncd runs the reusable blockoracle module as a standalone
// HTTP/SSE service.
//
// In the testenv it is the single authoritative publisher of block
// headers; all devshardd-testenv instances subscribe to it. In
// production, decentralized-api mounts the same module in-process, so
// this binary is testenv-only.
//
// Phase 3; see devshard/docs/testenv.md.
package main

func main() {
	// TODO(phase-3): call blockoracle/standalone.Run(ctx, cfg).
}
