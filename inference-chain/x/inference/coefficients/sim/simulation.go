package main

import (
	"math/rand"
	"strconv"

	mathsdk "cosmossdk.io/math"
	coefficient "github.com/productscience/inference/x/inference/coefficients"
	"github.com/productscience/inference/x/inference/types"
)

type position struct{ host, node int }

func buildHardware(cfg config, rng *rand.Rand) [][]string {
	var pool []string
	for _, gpu := range sortedKeys(cfg.GPUCounts) {
		for range cfg.GPUCounts[gpu] {
			pool = append(pool, gpu)
		}
	}
	rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	hardware := make([][]string, cfg.Hosts)
	for i, gpu := range pool {
		hardware[i%cfg.Hosts] = append(hardware[i%cfg.Hosts], gpu)
	}
	return hardware
}

func initialAllocation(hardware [][]string, baseModel string) [][]string {
	allocation := make([][]string, len(hardware))
	for host := range hardware {
		allocation[host] = make([]string, len(hardware[host]))
		for node := range hardware[host] {
			allocation[host][node] = baseModel
		}
	}
	return allocation
}

func stabilize(
	cfg config,
	modelIDs []string,
	hardware [][]string,
	allocation [][]string,
	controllerParams *types.DynamicCoefficientParams,
	state []*types.ConfirmationWeightScale,
	epsilon mathsdk.LegacyDec,
	rng *rand.Rand,
) int {
	var positions []position
	for host := range hardware {
		for node := range hardware[host] {
			positions = append(positions, position{host, node})
		}
	}
	totals := rawTotals(cfg, modelIDs, hardware, allocation)
	for pass := 1; pass <= cfg.MaxPasses; pass++ {
		moved := false
		for _, index := range rng.Perm(len(positions)) {
			pos := positions[index]
			gpu := hardware[pos.host][pos.node]
			current := allocation[pos.host][pos.node]
			effective, err := coefficient.EffectiveForAllocation(controllerParams, state, totals, modelIDs)
			check(err)
			best := current
			bestValue := value(cfg, current, gpu, effective[current])
			for _, candidate := range modelIDs {
				candidateValue := value(cfg, candidate, gpu, effective[candidate])
				if candidateValue.GT(bestValue) {
					best, bestValue = candidate, candidateValue
				}
			}
			currentValue := value(cfg, current, gpu, effective[current])
			if best != current && bestValue.GT(currentValue.Mul(mathsdk.LegacyOneDec().Add(epsilon))) {
				totals[current] -= cfg.Throughput[current][gpu]
				totals[best] += cfg.Throughput[best][gpu]
				allocation[pos.host][pos.node] = best
				moved = true
			}
		}
		if !moved {
			return pass
		}
	}
	return cfg.MaxPasses
}

func rawTotals(cfg config, modelIDs []string, hardware, allocation [][]string) map[string]int64 {
	result := make(map[string]int64, len(modelIDs))
	for host := range hardware {
		for node, gpu := range hardware[host] {
			result[allocation[host][node]] += cfg.Throughput[allocation[host][node]][gpu]
		}
	}
	return result
}

func snapshotEpoch(
	n int,
	cfg config,
	modelIDs []string,
	raw map[string]int64,
	state []*types.ConfirmationWeightScale,
	effective map[string]mathsdk.LegacyDec,
	passes int,
) epoch {
	shares := shares(cfg, raw)
	base := make(map[string]float64, len(modelIDs))
	for _, modelState := range state {
		base[modelState.ModelId] = asFloat(mustProtoDec(modelState.BaseCoefficient))
	}
	effectiveValues := make(map[string]float64, len(modelIDs))
	for _, modelID := range modelIDs {
		effectiveValues[modelID] = asFloat(effective[modelID])
	}
	reference := value(cfg, cfg.BaseModel, "H100", effective[cfg.BaseModel])
	gpuReward := make(map[string]float64, len(cfg.GPUCounts))
	for gpu := range cfg.GPUCounts {
		best := mathsdk.LegacyZeroDec()
		for _, modelID := range modelIDs {
			candidate := value(cfg, modelID, gpu, effective[modelID])
			if candidate.GT(best) {
				best = candidate
			}
		}
		gpuReward[gpu] = asFloat(best.Quo(reference))
	}
	return epoch{
		N: n, Shares: shares, Base: base, Effective: effectiveValues,
		GPUReward: gpuReward, Passes: passes,
	}
}

func shares(cfg config, raw map[string]int64) map[string]float64 {
	normalized := make(map[string]mathsdk.LegacyDec)
	total := mathsdk.LegacyZeroDec()
	for _, model := range cfg.Models {
		normalized[model.ID] = mustDec(model.Difficulty).MulInt64(raw[model.ID])
		total = total.Add(normalized[model.ID])
	}
	result := make(map[string]float64, len(normalized))
	for modelID, value := range normalized {
		if !total.IsZero() {
			result[modelID] = asFloat(value.Quo(total))
		}
	}
	return result
}

func value(cfg config, modelID, gpu string, coeff mathsdk.LegacyDec) mathsdk.LegacyDec {
	return coeff.MulInt64(cfg.Throughput[modelID][gpu])
}

func mustProtoDec(value *types.Decimal) mathsdk.LegacyDec {
	result, err := value.ToLegacyDec()
	check(err)
	return result
}

func asFloat(value mathsdk.LegacyDec) float64 {
	result, err := strconv.ParseFloat(value.String(), 64)
	check(err)
	return result
}
