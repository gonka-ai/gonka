// Package mockdapi assembles the dapi-facing interfaces that devshardd
// needs (BlockOracle, NodeManager) into a single in-process library.
//
// devshardd-testenv links this package directly; there is no loopback
// HTTP hop inside the container. In production, real decentralized-api
// provides the same interfaces in-process, so swapping mockdapi for
// the real dapi wiring is a single-line change at the call site
// (typically `mockdapi.New(...)` → `dapi.New(...)` with the same
// returned fields).
//
// Strict dependency rule: this package MAY import
// devshard/blockoracle/* and devshard/mlnode/gen (the generated
// NodeManager protobuf contract); it MUST NOT import anything under
// devshard/testenv/cmd. The reverse — host.NewHost etc. — MUST NOT
// import mockdapi either, so no prod build can accidentally link the
// testenv stubs. Enforced by the dependency check extended in
// devshard/docs/testenv.md §8.4.
//
// See devshard/docs/testenv.md §5 (library design) and §Phase 6
// (implementation plan).
package mockdapi
