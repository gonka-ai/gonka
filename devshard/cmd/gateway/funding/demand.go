package funding

const demandSamples = 40

// DemandWindow keeps a model's reserved money at its last forty scheduled ticks, ten minutes at the tick interval. See README.md, "Demand".
type DemandWindow struct {
	samples [demandSamples]uint64
	count   int
	next    int
}

// Record adds one scheduled tick's reserved money, dropping the oldest sample once the window is full.
func (window *DemandWindow) Record(reserved uint64) {
	window.samples[window.next] = reserved
	window.next = (window.next + 1) % demandSamples
	window.count = min(window.count+1, demandSamples)
}

// Peak is the most money the model held reserved at any recorded tick.
func (window *DemandWindow) Peak() uint64 {
	var peak uint64
	for _, sample := range window.samples[:window.count] {
		peak = max(peak, sample)
	}
	return peak
}

// Full reports whether ten minutes of ticks are recorded, which the surplus rule waits for.
func (window *DemandWindow) Full() bool { return window.count == demandSamples }
