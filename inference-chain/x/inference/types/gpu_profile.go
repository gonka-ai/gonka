package types

import (
	"fmt"
	"sort"
	"strings"
)

// non-accelerator hardware stripped from the GPU profile
var nonGpuHardwareTypes = map[string]struct{}{
	"CPU": {}, "RAM": {}, "MEMORY": {}, "DISK": {}, "SSD": {}, "HDD": {},
	"STORAGE": {}, "NVME": {}, "NIC": {}, "NETWORK": {},
}

func CanonicalGpuProfileId(node *HardwareNode) string {
	return CanonicalGpuProfileIdOf(node.GetHardware())
}

func CanonicalGpuProfileIdOf(hardware []*Hardware) string {
	counts := make(map[string]uint32)
	for _, h := range hardware {
		typ := strings.Join(strings.Fields(strings.ToUpper(strings.TrimSpace(h.GetType()))), " ")
		if typ == "" {
			continue
		}
		if _, skip := nonGpuHardwareTypes[typ]; skip {
			continue
		}
		counts[typ] += h.GetCount()
	}
	typeKeys := make([]string, 0, len(counts))
	for typ := range counts {
		typeKeys = append(typeKeys, typ)
	}
	sort.Strings(typeKeys)
	parts := make([]string, len(typeKeys))
	for i, typ := range typeKeys {
		parts[i] = fmt.Sprintf("%s x%d", typ, counts[typ])
	}
	return strings.Join(parts, " | ")
}
