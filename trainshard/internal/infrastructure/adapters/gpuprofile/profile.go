package gpuprofile

import (
	"fmt"
	"sort"

	"github.com/productscience/inference/x/inference/types"

	"trainshard/internal/domain/shared/vo"
)

type Card struct {
	Name      string
	MemoryMiB int
}

func Type(card Card) string {
	return fmt.Sprintf("%s | %dGB", card.Name, card.MemoryMiB/1024)
}

func FromCards(cards []Card) vo.GPUInventory {
	counts := make(map[string]uint32, len(cards))
	for _, card := range cards {
		counts[Type(card)]++
	}
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	hardware := make([]*types.Hardware, 0, len(kinds))
	for _, kind := range kinds {
		hardware = append(hardware, &types.Hardware{Type: kind, Count: counts[kind]})
	}
	return vo.GPUInventory{Profile: types.CanonicalGpuProfileIdOf(hardware), Count: len(cards)}
}

func FromHardware(hardware []*types.Hardware) vo.GPUInventory {
	return vo.GPUInventory{Profile: types.CanonicalGpuProfileIdOf(hardware)}
}

func Declared(kind string, count int) vo.GPUInventory {
	if count <= 0 || kind == "" {
		return vo.GPUInventory{}
	}
	return vo.GPUInventory{
		Profile: types.CanonicalGpuProfileIdOf([]*types.Hardware{{Type: kind, Count: uint32(count)}}),
		Count:   count,
	}
}
