package api

import (
	"fmt"
	"runtime/metrics"
	"slices"
)

type memoryReading struct {
	HeapAlloc     uint64
	TotalAlloc    uint64
	Sys           uint64
	HeapSys       uint64
	HeapIdle      uint64
	HeapInuse     uint64
	HeapReleased  uint64
	HeapObjects   uint64
	StackInuse    uint64
	NextGC        uint64
	NumGC         uint64
	GCCPUFraction float64
}

const (
	memoryHeapObjectBytes = "/memory/classes/heap/objects:bytes"
	memoryHeapUnused      = "/memory/classes/heap/unused:bytes"
	memoryHeapFree        = "/memory/classes/heap/free:bytes"
	memoryHeapReleased    = "/memory/classes/heap/released:bytes"
	memoryHeapObjects     = "/gc/heap/objects:objects"
	memoryHeapAllocated   = "/gc/heap/allocs:bytes"
	memoryStacks          = "/memory/classes/heap/stacks:bytes"
	memoryTotal           = "/memory/classes/total:bytes"
	memoryGCGoal          = "/gc/heap/goal:bytes"
	memoryGCCycles        = "/gc/cycles/total:gc-cycles"
	memoryGCCPU           = "/cpu/classes/gc/total:cpu-seconds"
	memoryTotalCPU        = "/cpu/classes/total:cpu-seconds"
)

func readMemory() (memoryReading, error) {
	counters := []string{memoryHeapObjectBytes, memoryHeapUnused, memoryHeapFree, memoryHeapReleased, memoryHeapObjects,
		memoryHeapAllocated, memoryStacks, memoryTotal, memoryGCGoal, memoryGCCycles}
	seconds := []string{memoryGCCPU, memoryTotalCPU}
	samples := make([]metrics.Sample, 0, len(counters)+len(seconds))
	for _, name := range slices.Concat(counters, seconds) {
		samples = append(samples, metrics.Sample{Name: name})
	}
	metrics.Read(samples)
	values := make(map[string]metrics.Value, len(samples))
	for _, sample := range samples {
		values[sample.Name] = sample.Value
	}
	for _, name := range counters {
		if kind := values[name].Kind(); kind != metrics.KindUint64 {
			return memoryReading{}, fmt.Errorf("runtime metric %s is %v, want uint64", name, kind)
		}
	}
	for _, name := range seconds {
		if kind := values[name].Kind(); kind != metrics.KindFloat64 {
			return memoryReading{}, fmt.Errorf("runtime metric %s is %v, want float64", name, kind)
		}
	}
	inuse := values[memoryHeapObjectBytes].Uint64() + values[memoryHeapUnused].Uint64()
	idle := values[memoryHeapFree].Uint64() + values[memoryHeapReleased].Uint64()
	reading := memoryReading{
		HeapAlloc:    values[memoryHeapObjectBytes].Uint64(),
		TotalAlloc:   values[memoryHeapAllocated].Uint64(),
		Sys:          values[memoryTotal].Uint64(),
		HeapSys:      inuse + idle,
		HeapIdle:     idle,
		HeapInuse:    inuse,
		HeapReleased: values[memoryHeapReleased].Uint64(),
		HeapObjects:  values[memoryHeapObjects].Uint64(),
		StackInuse:   values[memoryStacks].Uint64(),
		NextGC:       values[memoryGCGoal].Uint64(),
		NumGC:        values[memoryGCCycles].Uint64(),
	}
	if total := values[memoryTotalCPU].Float64(); total > 0 {
		reading.GCCPUFraction = values[memoryGCCPU].Float64() / total
	}
	return reading, nil
}
