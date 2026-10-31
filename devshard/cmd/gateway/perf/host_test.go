package perf

import (
	"math"
	"testing"
	"time"
)

// testEpoch is a fixed reference instant; every test time is derived from it via Add.
var testEpoch = time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

func almostEqual(a, b, epsilon float64) bool {
	return math.Abs(a-b) <= epsilon
}

// Test flow:
//  1. Build a decayed counter with a 10-minute half-life.
//  2. Assert its value at the epoch, before any add, is 0.
func TestDecayedCounterValueBeforeAnyAddIsZero(t *testing.T) {
	c := newDecayedCounter(10 * time.Minute)
	if got := c.value(testEpoch); got != 0 {
		t.Fatalf("value() before any add = %v, want 0", got)
	}
}

// Test flow:
//  1. Build a decayed counter.
//  2. Add to it three times at the same instant.
//  3. Assert its value at that instant is 3.
func TestDecayedCounterAddAccumulatesAtSameInstant(t *testing.T) {
	c := newDecayedCounter(10 * time.Minute)
	c.add(testEpoch)
	c.add(testEpoch)
	c.add(testEpoch)
	if got := c.value(testEpoch); got != 3 {
		t.Fatalf("value() after 3 adds at the same instant = %v, want 3", got)
	}
}

// Test flow:
//  1. Build a decayed counter with a fixed half-life.
//  2. Add to it once at the epoch.
//  3. Assert its value one half-life later is approximately 0.5.
func TestDecayedCounterValueAfterOneHalfLifeIsHalved(t *testing.T) {
	const halfLife = 10 * time.Minute
	c := newDecayedCounter(halfLife)
	c.add(testEpoch)
	got := c.value(testEpoch.Add(halfLife))
	if !almostEqual(got, 0.5, 1e-9) {
		t.Fatalf("value() one half-life after a single add = %v, want 0.5", got)
	}
}

// Test flow:
//  1. Build a host-perf tracker.
//  2. Record three non-responsive samples at increasing instants.
//  3. Assert `consecutiveFail` is 3.
func TestHostPerfConsecutiveFailIncrementsOnEachNonResponsiveSample(t *testing.T) {
	h := newHostPerf(10 * time.Minute)
	fail := Sample{Responsive: false}

	h.recordSample(fail, testEpoch)
	h.recordSample(fail, testEpoch.Add(time.Second))
	h.recordSample(fail, testEpoch.Add(2*time.Second))

	if h.consecutiveFail != 3 {
		t.Fatalf("consecutiveFail after 3 failures = %d, want 3", h.consecutiveFail)
	}
}

// Test flow:
//  1. Build a host-perf tracker.
//  2. Record two failing samples, then one responsive sample.
//  3. Assert `consecutiveFail` resets to 0.
func TestHostPerfConsecutiveFailResetsToZeroOnResponsiveSample(t *testing.T) {
	h := newHostPerf(10 * time.Minute)
	fail := Sample{Responsive: false}
	success := Sample{Responsive: true}

	h.recordSample(fail, testEpoch)
	h.recordSample(fail, testEpoch.Add(time.Second))
	h.recordSample(success, testEpoch.Add(2*time.Second))

	if h.consecutiveFail != 0 {
		t.Fatalf("consecutiveFail after a responsive sample = %d, want 0 (reset)", h.consecutiveFail)
	}
}

// Test flow:
//  1. Build a host-perf tracker.
//  2. Record one failing sample at a fixed time.
//  3. Assert `lastSeen` is updated to that time.
func TestHostPerfRecordSampleUpdatesLastSeenEvenOnFailure(t *testing.T) {
	h := newHostPerf(10 * time.Minute)
	failTime := testEpoch.Add(5 * time.Minute)

	h.recordSample(Sample{Responsive: false}, failTime)

	if !h.lastSeen.Equal(failTime) {
		t.Fatalf("lastSeen after a failing sample = %v, want %v", h.lastSeen, failTime)
	}
}

// Test flow:
//  1. Build a host-perf tracker with no recorded samples.
//  2. Assert `failureRate` returns rate 0 and volume 0 rather than NaN.
func TestHostPerfFailureRateZeroVolumeReturnsZeroRateNotNaN(t *testing.T) {
	h := newHostPerf(10 * time.Minute)

	rate, volume := h.failureRate(testEpoch)

	if rate != 0 || volume != 0 {
		t.Fatalf("failureRate() with no samples = (%v, %v), want (0, 0)", rate, volume)
	}
}

// Test flow:
//  1. Build a host-perf tracker.
//  2. Record three failing samples at the same instant.
//  3. Assert `failureRate` reports rate 1 and decayed volume 3.
func TestHostPerfFailureRateAllFailuresAtSameInstant(t *testing.T) {
	h := newHostPerf(10 * time.Minute)
	fail := Sample{Responsive: false}

	h.recordSample(fail, testEpoch)
	h.recordSample(fail, testEpoch)
	h.recordSample(fail, testEpoch)

	rate, volume := h.failureRate(testEpoch)
	if rate != 1 {
		t.Fatalf("failureRate() with only failures = %v, want 1", rate)
	}
	if volume != 3 {
		t.Fatalf("failureRate() decayed volume with only failures = %v, want 3", volume)
	}
}

// Test flow:
//  1. Build a host-perf tracker with a fixed half-life.
//  2. Record three failing samples at the epoch.
//  3. Record one successful sample one half-life later.
//  4. Assert `failureRate` at that later instant reports rate 0.6 and decayed volume 2.5 (the three failures decay to 1.5, the fresh success adds 1, for rate 1.5/2.5 and volume 2.5).
func TestHostPerfFailureRateDecaysAsSuccessesFollowFailures(t *testing.T) {
	const halfLife = 10 * time.Minute
	h := newHostPerf(halfLife)
	fail := Sample{Responsive: false}
	success := Sample{Responsive: true}

	h.recordSample(fail, testEpoch)
	h.recordSample(fail, testEpoch)
	h.recordSample(fail, testEpoch)

	later := testEpoch.Add(halfLife)
	h.recordSample(success, later)

	rate, volume := h.failureRate(later)
	if !almostEqual(rate, 0.6, 1e-9) {
		t.Fatalf("failureRate() rate after decay = %v, want 0.6", rate)
	}
	if !almostEqual(volume, 2.5, 1e-9) {
		t.Fatalf("failureRate() decayed volume = %v, want 2.5", volume)
	}
}
