//go:build blockoracle_compile_check

// Package blockoraclecompile exists only for the §8.4 dapi import sanity check: it
// is a leaf package (no dapi / cosmos imports) that pins
// devshard/blockoracle/observer.NewTendermint. The rest of
// internal/devshard must not be pulled in here or the check would compile the
// world.
//
// Normal dapi builds omit this package (build tag). `make ci-dep-check` in
// devshard runs: go build -tags=blockoracle_compile_check ./internal/devshard/blockoraclecompile/
package blockoraclecompile

import "devshard/blockoracle/observer"

var _ = observer.NewTendermint
