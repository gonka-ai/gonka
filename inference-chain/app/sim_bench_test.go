//go:build simsbench

package app_test

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/testutil/simsx"
	simcli "github.com/cosmos/cosmos-sdk/x/simulation/client/cli"
)

// BenchmarkFullAppSimulation profiles a single full-simulation run.
//
// A full simulation takes minutes, so b.N looping is not meaningful and
// the benchmark is single-iteration. Pass `-benchtime=1x`. Per-iteration
// metrics (ns/op, allocs/op) are not useful; this benchmark exists for
// `-cpuprofile`, `-memprofile`, and `-trace`.
//
// Profile with:
//
//	go test -tags 'sims simsbench' -run=^$ \
//	    github.com/productscience/inference/app \
//	    -bench ^BenchmarkFullAppSimulation$ -benchtime=1x -cpuprofile cpu.out
func BenchmarkFullAppSimulation(b *testing.B) {
	config := simcli.NewConfigFromFlags()
	config.ChainID = simsx.SimAppChainID

	seed := config.Seed
	if seed == simcli.DefaultSeedValue {
		seed = defaultSimSeeds[0]
	}
	simsx.RunWithSeed(b, config, NewSimApp, setupStateFactory, seed, nil)
}
