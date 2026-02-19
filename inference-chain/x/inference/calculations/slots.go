package calculations

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"
)

type WeightEntry struct {
	Address string
	Weight  int64
}

type slotRandom struct {
	randomVal int64
	origIdx   int
}

// PrepareSortedEntries filters and sorts weights, returns sorted entries and total weight.
// Returns nil, 0 if weights is empty or all weights are non-positive.
func PrepareSortedEntries(weights map[string]int64) ([]WeightEntry, int64) {
	if len(weights) == 0 {
		return nil, 0
	}

	keys := make([]string, 0, len(weights))
	for addr := range weights {
		keys = append(keys, addr)
	}
	slices.Sort(keys)

	sortedEntries := make([]WeightEntry, 0, len(keys))
	var totalWeight int64
	for _, addr := range keys {
		w := weights[addr]
		if w <= 0 {
			continue
		}
		sortedEntries = append(sortedEntries, WeightEntry{addr, w})
		totalWeight += w
	}

	if totalWeight == 0 || len(sortedEntries) == 0 {
		return nil, 0
	}

	return sortedEntries, totalWeight
}

// GetSlotsFromSorted uses pre-sorted entries to avoid sorting per call.
func GetSlotsFromSorted(appHash, participantAddress string, sortedEntries []WeightEntry, totalWeight int64, nSlots int) []string {
	if nSlots == 0 || totalWeight <= 0 {
		return nil
	}

	randoms := make([]slotRandom, nSlots)
	for i := 0; i < nSlots; i++ {
		randoms[i] = slotRandom{
			randomVal: slotRandomVal(appHash, participantAddress, i, totalWeight),
			origIdx:   i,
		}
	}
	slices.SortFunc(randoms, func(a, b slotRandom) int {
		return cmp.Compare(a.randomVal, b.randomVal)
	})

	result := make([]string, nSlots)
	cumulative := int64(0)
	randIdx := 0

	for _, entry := range sortedEntries {
		cumulative += entry.Weight
		for randIdx < len(randoms) && randoms[randIdx].randomVal < cumulative {
			result[randoms[randIdx].origIdx] = entry.Address
			randIdx++
		}
	}

	return result
}

// GetSlotFromSorted returns a single slot by index using pre-sorted entries.
func GetSlotFromSorted(appHash, participantAddress string, sortedEntries []WeightEntry, totalWeight int64, slotIdx int) string {
	if len(sortedEntries) == 0 || totalWeight <= 0 {
		return ""
	}

	randomVal := slotRandomVal(appHash, participantAddress, slotIdx, totalWeight)

	cumulative := int64(0)
	for _, entry := range sortedEntries {
		cumulative += entry.Weight
		if randomVal < cumulative {
			return entry.Address
		}
	}

	return sortedEntries[len(sortedEntries)-1].Address
}

func slotRandomVal(appHash, participantAddress string, slotIdx int, totalWeight int64) int64 {
	seedData := fmt.Sprintf("%s%s%d", appHash, participantAddress, slotIdx)
	hash := sha256.Sum256([]byte(seedData))
	// Use uint64 for modulo to avoid negative values
	return int64(binary.BigEndian.Uint64(hash[:8]) % uint64(totalWeight))
}
