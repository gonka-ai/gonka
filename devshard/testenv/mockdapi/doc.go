// Package mockdapi assembles the dapi-facing interfaces that devshardd
// needs (BlockOracle, NodeManager) into a single in-process library.
//
// devshardd-testenv links this package directly; there is no loopback HTTP
// hop inside the container. In production, real decentralized-api
// provides the same interfaces in-process, so swapping mockdapi for the
// real dapi wiring is a single-line change at the call site.
//
// Strict dependency rule: this package MAY import devshard/blockoracle/*
// and devshard core types; it MUST NOT import anything under
// devshard/testenv/cmd. See devshard/docs/testenv.md §5.
package mockdapi
