package perf

import (
	"testing"
	"time"
)

// Test flow:
//  1. Build an empty latency window.
//  2. Add samples one at a time up to one short of the minimum sample count, asserting `p75` reports unknown each time.
//  3. Add one more sample to reach the minimum and assert `p75` now reports known.
func TestLatencyWindow_SaysNothingUntilItHasAHistory(t *testing.T) {
	t.Parallel()
	var window latencyWindow

	for count := 1; count < latencyWindowMinimum; count++ {
		window.add(time.Second)
		if _, known := window.p75(latencyWindowMinimum); known {
			t.Fatalf("answered after %d samples, wants %d", count, latencyWindowMinimum)
		}
	}

	window.add(time.Second)
	if _, known := window.p75(latencyWindowMinimum); !known {
		t.Error("the window holds enough samples and must answer")
	}
}

// Test flow:
//  1. Build a latency window and add samples of 1s through 20s.
//  2. Assert `p75` reports known and returns 16 seconds.
func TestLatencyWindow_ReportsTheQuartileNotTheAverage(t *testing.T) {
	t.Parallel()
	var window latencyWindow

	for i := 1; i <= 20; i++ {
		window.add(time.Duration(i) * time.Second)
	}

	observed, known := window.p75(latencyWindowMinimum)
	if !known {
		t.Fatal("the window must answer")
	}
	if observed != 16*time.Second {
		t.Errorf("p75 = %v, want 16s", observed)
	}
}

// Test flow:
//  1. Fill the window's full ring capacity with 5-minute samples.
//  2. Fill it again with 2-second samples, aging the slow ones out.
//  3. Assert `p75` reports 2 seconds.
func TestLatencyWindow_ForgetsWhatFellOutOfTheRing(t *testing.T) {
	t.Parallel()
	var window latencyWindow

	for range latencyWindowSize {
		window.add(5 * time.Minute)
	}
	for range latencyWindowSize {
		window.add(2 * time.Second)
	}

	observed, _ := window.p75(latencyWindowMinimum)
	if observed != 2*time.Second {
		t.Errorf("p75 = %v, want 2s once the slow history aged out", observed)
	}
}

// Test flow:
//  1. Build a host-perf tracker.
//  2. Record enough responsive samples carrying a 4-second first-content latency to satisfy the minimum, then record one responsive sample with no latency.
//  3. Assert the host's first-content `p75` still reports 4 seconds, from the samples that carried one.
func TestRecordSample_KeepsTheLatencyItWasGiven(t *testing.T) {
	t.Parallel()
	host := newHostPerf(time.Minute)

	for range latencyWindowMinimum {
		host.recordSample(Sample{Responsive: true, FirstContent: 4 * time.Second}, time.Now())
	}
	host.recordSample(Sample{Responsive: true}, time.Now())

	observed, known := host.firstContent.p75(latencyWindowMinimum)
	if !known || observed != 4*time.Second {
		t.Errorf("p75 = %v (known=%v), want 4s from the samples that carried one", observed, known)
	}
}

// Test flow:
//  1. Build a latency window and add one fewer sample than the minimum.
//  2. Assert `pressure` reports 0: a window too young to name a quantile cannot name a baseline either.
func TestLatencyWindow_SaysNothingAboutPressureUntilItHasAHistory(t *testing.T) {
	t.Parallel()
	var window latencyWindow

	for range latencyWindowMinimum - 1 {
		window.add(time.Second)
	}

	if pressure := window.pressure(); pressure != 0 {
		t.Errorf("pressure = %v, want 0: a window too young to name a quantile cannot name a baseline either", pressure)
	}
}

// Test flow:
//  1. Fill the window's full ring capacity with 1-second samples.
//  2. Assert `pressure` reports 1: a host holding the latency it always held is not congested.
func TestLatencyWindow_ReportsAHostAtItsOwnBestAsUnpressured(t *testing.T) {
	t.Parallel()
	var window latencyWindow

	for range latencyWindowSize {
		window.add(time.Second)
	}

	if pressure := window.pressure(); pressure != 1 {
		t.Errorf("pressure = %v, want 1: a host holding the latency it always held is not congested", pressure)
	}
}

// Test flow:
//  1. Fill the window with 1-second samples, then fill it again with 2-second samples.
//  2. Assert `pressure` reports above 1.5: a host now twice as slow as its best is congested before it has failed anything.
func TestLatencyWindow_ReportsSlowingDownAgainstTheBestItHeld(t *testing.T) {
	t.Parallel()
	var window latencyWindow

	for range latencyWindowSize {
		window.add(time.Second)
	}
	for range latencyWindowSize {
		window.add(2 * time.Second)
	}

	if pressure := window.pressure(); pressure <= 1.5 {
		t.Errorf("pressure = %v, want above 1.5: a host now twice as slow as its best is congested before it has failed anything", pressure)
	}
}

// Test flow:
//  1. Fill the window with 10-second samples, then fill it again with faster 1-second samples.
//  2. Assert `pressure` reports 1: the best the host has held becomes what it is measured against, at once.
func TestLatencyWindow_TakesANewBestImmediately(t *testing.T) {
	t.Parallel()
	var window latencyWindow

	for range latencyWindowSize {
		window.add(10 * time.Second)
	}
	for range latencyWindowSize {
		window.add(time.Second)
	}

	if pressure := window.pressure(); pressure != 1 {
		t.Errorf("pressure = %v, want 1: the best a host has held is the one it is measured against", pressure)
	}
}

// Test flow:
//  1. Fill the window with 1-second samples, then with 2-second samples, and record the settled pressure.
//  2. Add 20 more full rings of 2-second samples.
//  3. Assert the pressure afterward is lower than the settled value: the baseline rises toward the latency the host now sustains.
func TestLatencyWindow_ForgetsALuckyMinimumOverTime(t *testing.T) {
	t.Parallel()
	var window latencyWindow

	for range latencyWindowSize {
		window.add(time.Second)
	}
	for range latencyWindowSize {
		window.add(2 * time.Second)
	}
	settled := window.pressure()

	for range 20 * latencyWindowSize {
		window.add(2 * time.Second)
	}

	if drifted := window.pressure(); !(drifted < settled) {
		t.Errorf("pressure = %v after a long run at the new latency, want below the %v it started at: the baseline rises towards what the host now holds", drifted, settled)
	}
}
