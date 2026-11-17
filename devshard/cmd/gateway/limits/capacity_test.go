package limits

import (
	"sync"
	"testing"

	"devshard/cmd/gateway/chain"
)

// Test flow:
//  1. Build a capacity with `NewCapacity`, varying the availability callback across subtests.
//  2. Update it with a snapshot that varies per subtest: full by-model and generic weights, a partial by-model view (whose full weight falls back to the generic sum), a blocked flag, or no snapshot update at all.
//  3. Read `ModelWeights` for a chosen model and assert its ScaleFactor.
func TestCapacityScaleFactor(t *testing.T) {
	baseSnapshot := func() chain.PhaseSnapshot {
		return chain.PhaseSnapshot{
			CurrentWeightsByModel: map[string]map[string]float64{
				"modelX": {"hostA": 40, "hostB": 20},
			},
			FullWeightsByModel: map[string]map[string]float64{
				"modelX": {"hostA": 60, "hostB": 40},
			},
			CurrentWeights: map[string]float64{"hostA": 40, "hostB": 20, "hostC": 40},
			FullWeights:    map[string]float64{"hostA": 60, "hostB": 40, "hostC": 100},
		}
	}

	t.Run("all hosts available uses W_tot over W_ref for the model", func(t *testing.T) {
		t.Parallel()
		capacity := NewCapacity(nil)
		capacity.Update(baseSnapshot())
		if got := capacity.ModelWeights("modelX", false).ScaleFactor; got != 0.6 {
			t.Errorf("ModelWeights(modelX).ScaleFactor = %v, want 0.6", got)
		}
	})

	t.Run("unavailable host drops from W_tot but not W_ref", func(t *testing.T) {
		t.Parallel()
		capacity := NewCapacity(func(participant, model string) bool {
			return participant != "hostB" || model != "modelX"
		})
		capacity.Update(baseSnapshot())
		if got := capacity.ModelWeights("modelX", false).ScaleFactor; got != 0.4 {
			t.Errorf("ModelWeights(modelX).ScaleFactor = %v, want 0.4", got)
		}
	})

	t.Run("model absent from a populated by-model view gets no capacity", func(t *testing.T) {
		t.Parallel()
		capacity := NewCapacity(nil)
		capacity.Update(baseSnapshot())
		if got := capacity.ModelWeights("modelNeverSeen", false).ScaleFactor; got != 0 {
			t.Errorf("ModelWeights(modelNeverSeen).ScaleFactor = %v, want 0: a model nobody serves must not inherit the generic view", got)
		}
		capacity.SetEscrowMembership("escrow1", map[string]float64{"hostA": 1})
		if got := capacity.EscrowWeight("escrow1", "modelNeverSeen"); got != 0 {
			t.Errorf("EscrowWeight(escrow1, modelNeverSeen) = %v, want 0", got)
		}
	})

	t.Run("with no by-model view at all the generic view applies to every model", func(t *testing.T) {
		t.Parallel()
		capacity := NewCapacity(nil)
		capacity.Update(chain.PhaseSnapshot{
			CurrentWeights: map[string]float64{"hostA": 40, "hostB": 60},
			FullWeights:    map[string]float64{"hostA": 100, "hostB": 100},
		})
		if got := capacity.ModelWeights("anyModel", false).ScaleFactor; got != 0.5 {
			t.Errorf("ModelWeights(anyModel).ScaleFactor = %v, want 0.5 (generic view, no per-model data yet)", got)
		}
	})

	t.Run("current-by-model entry is honored even without a matching full-by-model entry", func(t *testing.T) {
		t.Parallel()
		capacity := NewCapacity(nil)
		snapshot := baseSnapshot()
		snapshot.CurrentWeightsByModel["modelPartial"] = map[string]float64{"hostA": 50}
		capacity.Update(snapshot)
		if got := capacity.ModelWeights("modelPartial", false).ScaleFactor; got != 0.25 {
			t.Errorf("ModelWeights(modelPartial).ScaleFactor = %v, want 0.25 (real current 50 / generic full 200)", got)
		}
	})

	t.Run("a blocked caller forces scale to zero regardless of weights", func(t *testing.T) {
		t.Parallel()
		capacity := NewCapacity(nil)
		snapshot := baseSnapshot()
		snapshot.RequestsBlocked = true
		capacity.Update(snapshot)
		if got := capacity.ModelWeights("modelX", true).ScaleFactor; got != 0 {
			t.Errorf("ModelWeights(modelX, blocked).ScaleFactor = %v, want 0", got)
		}
		if got := capacity.ModelWeights("modelX", false).ScaleFactor; got == 0 {
			t.Error("ModelWeights(modelX, not blocked).ScaleFactor = 0 while the chain says blocked: relaxed mode cannot serve")
		}
	})

	t.Run("zero baseline before any Update means unlimited", func(t *testing.T) {
		t.Parallel()
		capacity := NewCapacity(nil)
		if got := capacity.ModelWeights("modelX", false).ScaleFactor; got != 1 {
			t.Errorf("ModelWeights(modelX).ScaleFactor = %v, want 1 (unlimited) before any Update", got)
		}
	})

	t.Run("nil available treats every host as available", func(t *testing.T) {
		t.Parallel()
		capacity := NewCapacity(nil)
		capacity.Update(baseSnapshot())
		if got := capacity.ModelWeights("modelX", false).ScaleFactor; got != 0.6 {
			t.Errorf("ModelWeights(modelX).ScaleFactor = %v, want 0.6 with nil availability", got)
		}
	})
}

// Test flow:
//  1. Define a shared per-model snapshot and an always-available function used by most cases.
//  2. For each case, vary the snapshot shape, availability function, model name, and blocked flag.
//  3. Update a fresh capacity with the case's snapshot.
//  4. Call `ModelWeights` and assert it equals the case's expected ModelWeights struct.
func TestCapacityModelWeights(t *testing.T) {
	byModelViews := chain.PhaseSnapshot{
		CurrentWeightsByModel: map[string]map[string]float64{"modelX": {"hostA": 40, "hostB": 20}},
		FullWeightsByModel:    map[string]map[string]float64{"modelX": {"hostA": 60, "hostB": 40}},
	}
	everyHostAvailable := func(string, string) bool { return true }

	cases := []struct {
		name      string
		snapshot  chain.PhaseSnapshot
		available func(participant, model string) bool
		model     string
		blocked   bool
		want      ModelWeights
	}{
		{
			name:      "every host available",
			snapshot:  byModelViews,
			available: everyHostAvailable,
			model:     "modelX",
			want:      ModelWeights{ScaleFactor: 0.6, CurrentWeight: 60, BaselineWeight: 100},
		},
		{
			name:      "an unavailable host leaves the current weight but not the baseline",
			snapshot:  byModelViews,
			available: func(participant, _ string) bool { return participant != "hostB" },
			model:     "modelX",
			want:      ModelWeights{ScaleFactor: 0.4, CurrentWeight: 40, BaselineWeight: 100},
		},
		{
			name:      "a blocked caller zeroes the scale and keeps the weights",
			snapshot:  byModelViews,
			available: everyHostAvailable,
			model:     "modelX",
			blocked:   true,
			want:      ModelWeights{ScaleFactor: 0, CurrentWeight: 60, BaselineWeight: 100},
		},
		{
			name:      "a model absent from a populated by-model view is served by nobody",
			snapshot:  byModelViews,
			available: everyHostAvailable,
			model:     "modelNeverSeen",
			want:      ModelWeights{},
		},
		{
			name:      "no observation at all is unobserved and unlimited",
			snapshot:  chain.PhaseSnapshot{},
			available: everyHostAvailable,
			model:     "modelX",
			want:      ModelWeights{ScaleFactor: 1, WeightsUnobserved: true},
		},
		{
			name: "empty views for a served model are unobserved",
			snapshot: chain.PhaseSnapshot{
				CurrentWeightsByModel: map[string]map[string]float64{"modelX": {}},
				FullWeightsByModel:    map[string]map[string]float64{"modelX": {}},
			},
			available: everyHostAvailable,
			model:     "modelX",
			want:      ModelWeights{ScaleFactor: 1, WeightsUnobserved: true},
		},
		{
			name: "a zero baseline is unlimited and still observed",
			snapshot: chain.PhaseSnapshot{
				CurrentWeights: map[string]float64{"hostA": 30, "hostB": 0},
				FullWeights:    map[string]float64{"hostA": 0, "hostB": 0},
			},
			available: everyHostAvailable,
			model:     "modelX",
			want:      ModelWeights{ScaleFactor: 1, CurrentWeight: 30, BaselineWeight: 0},
		},
		{
			name: "a current weight above the baseline clamps the scale to one",
			snapshot: chain.PhaseSnapshot{
				CurrentWeights: map[string]float64{"hostA": 150},
				FullWeights:    map[string]float64{"hostA": 100},
			},
			available: everyHostAvailable,
			model:     "modelX",
			want:      ModelWeights{ScaleFactor: 1, CurrentWeight: 150, BaselineWeight: 100},
		},
		{
			name: "the full view falls back to the generic one on its own",
			snapshot: chain.PhaseSnapshot{
				CurrentWeightsByModel: map[string]map[string]float64{"modelX": {"hostA": 50}},
				FullWeights:           map[string]float64{"hostA": 100, "hostB": 100},
			},
			available: everyHostAvailable,
			model:     "modelX",
			want:      ModelWeights{ScaleFactor: 0.25, CurrentWeight: 50, BaselineWeight: 200},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			capacity := NewCapacity(testCase.available)
			capacity.Update(testCase.snapshot)

			got := capacity.ModelWeights(testCase.model, testCase.blocked)

			if got != testCase.want {
				t.Errorf("ModelWeights(%s, blocked=%v) = %+v, want %+v", testCase.model, testCase.blocked, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Update a capacity with a first snapshot giving hostA a current and full weight.
//  2. Update it again with a second snapshot giving hostZ a different current and full weight.
//  3. Assert `ModelWeights` reflects only the second snapshot's scale factor.
func TestCapacityUpdateReplacesPriorSnapshot(t *testing.T) {
	t.Parallel()
	capacity := NewCapacity(nil)
	capacity.Update(chain.PhaseSnapshot{
		CurrentWeights: map[string]float64{"hostA": 10},
		FullWeights:    map[string]float64{"hostA": 100},
	})
	capacity.Update(chain.PhaseSnapshot{
		CurrentWeights: map[string]float64{"hostZ": 50},
		FullWeights:    map[string]float64{"hostZ": 100},
	})
	if got := capacity.ModelWeights("modelX", false).ScaleFactor; got != 0.5 {
		t.Errorf("ModelWeights(modelX).ScaleFactor = %v, want 0.5 from the second snapshot only", got)
	}
}

// Test flow:
//  1. Update a capacity with per-model current weights, varying the availability callback per subtest to eject a host or leave every host in.
//  2. Set an escrow's membership shares over those hosts, in some subtests calling `SetEscrowMembership` twice with different shares.
//  3. Call `EscrowWeight` and assert the weighted contribution, covering an ejected host, an unknown escrow, and a second `SetEscrowMembership` call replacing rather than merging.
func TestCapacityEscrowWeight(t *testing.T) {
	t.Run("membership share weighted by per-model current weight", func(t *testing.T) {
		t.Parallel()
		capacity := NewCapacity(nil)
		capacity.Update(chain.PhaseSnapshot{
			CurrentWeightsByModel: map[string]map[string]float64{
				"modelX": {"hostA": 100, "hostB": 200},
			},
		})
		capacity.SetEscrowMembership("escrow1", map[string]float64{"hostA": 0.5, "hostB": 0.25})
		if got := capacity.EscrowWeight("escrow1", "modelX"); got != 100 {
			t.Errorf("EscrowWeight(escrow1, modelX) = %v, want 100", got)
		}
	})

	t.Run("an ejected host drops its contribution", func(t *testing.T) {
		t.Parallel()
		capacity := NewCapacity(func(participant, model string) bool {
			return participant != "hostB" || model != "modelX"
		})
		capacity.Update(chain.PhaseSnapshot{
			CurrentWeightsByModel: map[string]map[string]float64{
				"modelX": {"hostA": 100, "hostB": 200},
			},
		})
		capacity.SetEscrowMembership("escrow1", map[string]float64{"hostA": 0.5, "hostB": 0.25})
		if got := capacity.EscrowWeight("escrow1", "modelX"); got != 50 {
			t.Errorf("EscrowWeight(escrow1, modelX) = %v, want 50 with hostB ejected", got)
		}
	})

	t.Run("unknown escrow returns zero", func(t *testing.T) {
		t.Parallel()
		capacity := NewCapacity(nil)
		capacity.Update(chain.PhaseSnapshot{
			CurrentWeightsByModel: map[string]map[string]float64{"modelX": {"hostA": 100}},
		})
		if got := capacity.EscrowWeight("neverRegistered", "modelX"); got != 0 {
			t.Errorf("EscrowWeight(neverRegistered, modelX) = %v, want 0", got)
		}
	})

	t.Run("SetEscrowMembership replaces rather than merges", func(t *testing.T) {
		t.Parallel()
		capacity := NewCapacity(nil)
		capacity.Update(chain.PhaseSnapshot{
			CurrentWeightsByModel: map[string]map[string]float64{
				"modelX": {"hostA": 100, "hostB": 200},
			},
		})
		capacity.SetEscrowMembership("escrow1", map[string]float64{"hostA": 1})
		capacity.SetEscrowMembership("escrow1", map[string]float64{"hostB": 1})
		if got := capacity.EscrowWeight("escrow1", "modelX"); got != 200 {
			t.Errorf("EscrowWeight(escrow1, modelX) = %v, want 200 (only the latest membership)", got)
		}
	})
}

// Test flow:
//  1. Update a capacity with a per-model current weight for hostA and register an escrow's full membership in it.
//  2. Assert `EscrowWeight` reflects that membership before removal.
//  3. Call `RemoveEscrow`.
//  4. Assert `EscrowWeight` returns zero after removal.
func TestCapacityRemoveEscrow(t *testing.T) {
	t.Parallel()
	capacity := NewCapacity(nil)
	capacity.Update(chain.PhaseSnapshot{
		CurrentWeightsByModel: map[string]map[string]float64{"modelX": {"hostA": 100}},
	})
	capacity.SetEscrowMembership("escrow1", map[string]float64{"hostA": 1})
	if got := capacity.EscrowWeight("escrow1", "modelX"); got != 100 {
		t.Fatalf("EscrowWeight(escrow1, modelX) = %v, want 100 before removal", got)
	}
	capacity.RemoveEscrow("escrow1")
	if got := capacity.EscrowWeight("escrow1", "modelX"); got != 0 {
		t.Errorf("EscrowWeight(escrow1, modelX) = %v, want 0 after RemoveEscrow", got)
	}
}

// Test flow:
//  1. Start goroutines that repeatedly call `Update` with new per-model weights, one host of which is excluded by the availability callback.
//  2. Start goroutines that repeatedly set an escrow's membership and read its `EscrowWeight`.
//  3. Start goroutines that repeatedly read `ModelWeights`' ScaleFactor.
//  4. Wait for every goroutine, then remove the escrow, relying on `go test -race` to catch any unsynchronized access.
func TestCapacityConcurrentUpdateAndRead(t *testing.T) {
	capacity := NewCapacity(func(participant, model string) bool { return participant != "hostEjected" })
	const goroutineCount = 8
	const iterationsPerGoroutine = 200

	var wg sync.WaitGroup
	wg.Add(goroutineCount * 3)

	for goroutineIndex := range goroutineCount {
		go func(goroutineIndex int) {
			defer wg.Done()
			for iteration := range iterationsPerGoroutine {
				capacity.Update(chain.PhaseSnapshot{
					CurrentWeightsByModel: map[string]map[string]float64{
						"modelX": {"hostA": float64(goroutineIndex + iteration), "hostEjected": 50},
					},
					FullWeightsByModel: map[string]map[string]float64{
						"modelX": {"hostA": 100, "hostEjected": 100},
					},
				})
			}
		}(goroutineIndex)

		go func() {
			defer wg.Done()
			for range iterationsPerGoroutine {
				capacity.SetEscrowMembership("escrowA", map[string]float64{"hostA": 1})
				_ = capacity.EscrowWeight("escrowA", "modelX")
			}
		}()

		go func() {
			defer wg.Done()
			for range iterationsPerGoroutine {
				_ = capacity.ModelWeights("modelX", false).ScaleFactor
			}
		}()
	}

	wg.Wait()
	capacity.RemoveEscrow("escrowA")
}

// Test flow:
//  1. Vary the chain snapshot across cases: no weights observed at all, an unavailable host among the escrow's shares, a stale snapshot that still carries prior weights, and weights observed as explicit zero.
//  2. Update a capacity, whose availability callback excludes hostUnavailable, with the case's snapshot and register the case's escrow shares.
//  3. Call `EscrowWeight` and assert it matches the case's expected fallback or observed weight.
func TestCapacityEscrowWeightOnUnobservedChainWeights(t *testing.T) {
	cases := []struct {
		name     string
		snapshot chain.PhaseSnapshot
		shares   map[string]float64
		want     float64
	}{
		{
			name:     "no weights observed at all falls back to the escrow's available share",
			snapshot: chain.PhaseSnapshot{LastUpdatedAt: testEpoch},
			shares:   map[string]float64{"hostA": 0.5, "hostB": 0.25},
			want:     0.75,
		},
		{
			name:     "an unavailable host is left out of the fallback share",
			snapshot: chain.PhaseSnapshot{LastUpdatedAt: testEpoch},
			shares:   map[string]float64{"hostA": 0.5, "hostUnavailable": 0.25},
			want:     0.5,
		},
		{
			name: "a stale snapshot keeps the weights it observed",
			snapshot: chain.PhaseSnapshot{
				CurrentWeights: map[string]float64{"hostA": 100},
				FullWeights:    map[string]float64{"hostA": 100},
				LastError:      "fetch participants: connection refused",
			},
			shares: map[string]float64{"hostA": 0.5},
			want:   50,
		},
		{
			name: "weights observed as zero stay zero",
			snapshot: chain.PhaseSnapshot{
				CurrentWeights: map[string]float64{"hostA": 0, "hostB": 0},
				FullWeights:    map[string]float64{"hostA": 0, "hostB": 0},
				LastUpdatedAt:  testEpoch,
			},
			shares: map[string]float64{"hostA": 0.5, "hostB": 0.25},
			want:   0,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			capacity := NewCapacity(func(participant, _ string) bool { return participant != "hostUnavailable" })
			capacity.Update(testCase.snapshot)
			capacity.SetEscrowMembership("escrowA", testCase.shares)

			if got := capacity.EscrowWeight("escrowA", "modelX"); got != testCase.want {
				t.Errorf("EscrowWeight() = %v, want %v", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build a capacity via `newCapacityWatchingItsLock`, whose availability callback tries to re-acquire the capacity's own lock and reports every host unavailable.
//  2. Call `ModelWeights` in one subtest and `EscrowWeight` in the other.
//  3. Assert the result is zero because the only host is unavailable.
//  4. Assert the availability callback never found the lock already held, i.e. it ran outside the critical section.
func TestCapacityAsksAvailabilityAfterReleasingItsLock(t *testing.T) {
	t.Run("model weights", func(t *testing.T) {
		t.Parallel()
		capacity, askedUnderLock := newCapacityWatchingItsLock()

		weights := capacity.ModelWeights("modelX", false)

		if weights.CurrentWeight != 0 {
			t.Fatalf("ModelWeights().CurrentWeight = %v, want 0: the only host is unavailable, so availability was never asked", weights.CurrentWeight)
		}
		if *askedUnderLock {
			t.Error("ModelWeights asked availability while holding the capacity lock")
		}
	})

	t.Run("escrow weight", func(t *testing.T) {
		t.Parallel()
		capacity, askedUnderLock := newCapacityWatchingItsLock()

		weight := capacity.EscrowWeight("escrow1", "modelX")

		if weight != 0 {
			t.Fatalf("EscrowWeight() = %v, want 0: the only host is unavailable, so availability was never asked", weight)
		}
		if *askedUnderLock {
			t.Error("EscrowWeight asked availability while holding the capacity lock")
		}
	})
}

// newCapacityWatchingItsLock answers every availability question "unavailable" and records whether one was asked with the capacity lock held.
func newCapacityWatchingItsLock() (*Capacity, *bool) {
	askedUnderLock := false
	var capacity *Capacity
	capacity = NewCapacity(func(string, string) bool {
		if capacity.mu.TryLock() {
			capacity.mu.Unlock()
		} else {
			askedUnderLock = true
		}
		return false
	})
	capacity.Update(chain.PhaseSnapshot{
		CurrentWeightsByModel: map[string]map[string]float64{"modelX": {"hostA": 100}},
		FullWeightsByModel:    map[string]map[string]float64{"modelX": {"hostA": 100}},
	})
	capacity.SetEscrowMembership("escrow1", map[string]float64{"hostA": 1})
	return capacity, &askedUnderLock
}
