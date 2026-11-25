package funding

import "testing"

// Test flow:
//  1. Record forty samples, the first of them the largest.
//  2. Assert the window is full and its peak is the first sample.
//  3. Record one more small sample and assert the peak moves off the sample that fell out.
func TestTheDemandWindowKeepsTheLastFortySamples(t *testing.T) {
	t.Parallel()
	var window DemandWindow
	window.Record(5_000)
	for range demandSamples - 1 {
		window.Record(10)
	}
	if !window.Full() || window.Peak() != 5_000 {
		t.Fatalf("Full(), Peak() = %v, %d, want true, 5000", window.Full(), window.Peak())
	}

	window.Record(10)

	if got := window.Peak(); got != 10 {
		t.Fatalf("Peak() = %d, want 10 once the largest sample fell out", got)
	}
}

// Test flow:
//  1. Record three samples into an empty window.
//  2. Assert it is not full and its peak is the largest of the three.
func TestADemandWindowWithFewSamplesIsNotFull(t *testing.T) {
	t.Parallel()
	var window DemandWindow
	for _, reserved := range []uint64{30, 90, 60} {
		window.Record(reserved)
	}

	if window.Full() || window.Peak() != 90 {
		t.Fatalf("Full(), Peak() = %v, %d, want false, 90", window.Full(), window.Peak())
	}
}
