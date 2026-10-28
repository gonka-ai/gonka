package scheduler

import (
	"testing"
	"time"
)

const (
	hostA = "participant-a"
	hostB = "participant-b"
)

var soleHost = []string{hostA}

var (
	baseTime        = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	matchWaitWindow = 200 * time.Millisecond
)

func queuedWaiter(enqueued time.Time, model string, excluded ...string) *waiter {
	queued := &waiter{
		profile:  RequestProfile{Model: model},
		exclude:  make(map[string]bool, len(excluded)),
		enqueued: enqueued,
	}
	for _, participant := range excluded {
		queued.exclude[participant] = true
	}
	return queued
}

func openAvailability() availability {
	return availability{
		pocRequired:  func(string) bool { return false },
		congested:    windowFullWhen(always(false)),
		ejected:      func(string) bool { return false },
		stateBlocked: func(string) bool { return false },
	}
}

func always(blocked bool) func(string) bool {
	return func(string) bool { return blocked }
}

// windowFullWhen states the limiter's verdict the way the drain reads it, so a test says "busy" and gets the block a full window earns.
func windowFullWhen(full func(string) bool) func(string) blockReason {
	return func(participant string) blockReason {
		if full(participant) {
			return blockWindowFull
		}
		return blockNone
	}
}

// Test flow:
//  1. For every combination of poc-required, congested, ejected, state-blocked and exclusion bits, build an `availability` and a queued waiter.
//  2. Compute `servable`'s verdict for the combination.
//  3. Compute whether `match` would serve the waiter on either participant.
//  4. Assert the two verdicts agree for every combination.
func TestServableAgreesWithMatchOverEveryFilterCombination(t *testing.T) {
	t.Parallel()
	participants := []string{hostA, hostB}

	for combination := range 1 << 6 {
		bit := func(index int) bool { return combination&(1<<index) != 0 }
		availability := availability{
			pocRequired:  func(participant string) bool { return bit(0) && participant == hostA },
			congested:    windowFullWhen(func(participant string) bool { return bit(1) && participant == hostB }),
			ejected:      func(participant string) bool { return bit(2) && participant == hostA },
			stateBlocked: func(participant string) bool { return bit(3) && participant == hostB },
		}
		var excluded []string
		if bit(5) {
			excluded = []string{hostB}
		}
		queued := queuedWaiter(baseTime, "model-a", excluded...)

		canServe, _, _, _ := servable(queued, participants, availability)
		matchWouldServe := false
		for _, participant := range participants {
			binding := HostBinding{Nonce: 1, Participant: participant}
			if _, serving := match(binding, []*waiter{queued}, soleHost, availability, baseTime, matchWaitWindow).(serve); serving {
				matchWouldServe = true
			}
		}

		if canServe != matchWouldServe {
			t.Fatalf("combination %06b: servable=%v but a match serve was %v", combination, canServe, matchWouldServe)
		}
	}
}

func decisionKind(decision Decision) string {
	switch decision.(type) {
	case serve:
		return "serve"
	case burn:
		return "burn"
	case hold:
		return "hold"
	case decline:
		return "decline"
	default:
		return ""
	}
}

func wantBurn(t *testing.T, decision Decision, kind GhostKind) {
	t.Helper()
	burned, ok := decision.(burn)
	if !ok {
		t.Fatalf("decision = %T (%v), want burn{%s}", decision, decision, kind.reason())
	}
	if burned.kind != kind {
		t.Fatalf("burn kind = %s, want %s", burned.kind.reason(), kind.reason())
	}
}

func wantServe(t *testing.T, decision Decision, expected *waiter) {
	t.Helper()
	served, ok := decision.(serve)
	if !ok {
		t.Fatalf("decision = %T (%v), want serve", decision, decision)
	}
	if served.waiter != expected {
		t.Fatalf("served waiter model %q, want %q", served.waiter.profile.Model, expected.profile.Model)
	}
}

// Test flow:
//  1. For each table case of poc-required and window-full flags, build an availability and match one waiter against a binding.
//  2. Assert poc required alone burns as `ghostPoC`.
//  3. Assert a full window alone burns as `ghostWindowFull`.
//  4. Assert poc wins when both are set.
//  5. Assert neither filter serves the waiter.
func TestMatchHostLevelFilters(t *testing.T) {
	t.Parallel()
	binding := HostBinding{Nonce: 7, HostIdx: 1, Participant: hostA}

	tests := []struct {
		name        string
		pocRequired bool
		windowFull  bool
		wantServed  bool
		wantKind    GhostKind
	}{
		{name: "poc required burns as poc ghost", pocRequired: true, wantKind: ghostPoC},
		{name: "a full window burns as a full window", windowFull: true, wantKind: ghostWindowFull},
		{name: "poc wins over a full window", pocRequired: true, windowFull: true, wantKind: ghostPoC},
		{name: "neither filter serves the waiter", wantServed: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			queued := queuedWaiter(baseTime, "model-a")
			availability := openAvailability()
			availability.pocRequired = always(testCase.pocRequired)
			availability.congested = windowFullWhen(always(testCase.windowFull))

			decision := match(binding, []*waiter{queued}, soleHost, availability, baseTime, matchWaitWindow)

			if testCase.wantServed {
				wantServe(t, decision, queued)
				return
			}
			wantBurn(t, decision, testCase.wantKind)
		})
	}
}

// Test flow:
//  1. Queue three compatible waiters at increasing enqueue times.
//  2. Match a binding against all three.
//  3. Assert the decision serves the oldest one.
func TestMatchServesOldestCompatibleWaiter(t *testing.T) {
	t.Parallel()
	oldest := queuedWaiter(baseTime, "model-a")
	middle := queuedWaiter(baseTime.Add(10*time.Millisecond), "model-b")
	newest := queuedWaiter(baseTime.Add(20*time.Millisecond), "model-c")
	binding := HostBinding{Nonce: 4, HostIdx: 0, Participant: hostA}

	decision := match(binding, []*waiter{oldest, middle, newest}, soleHost, openAvailability(), baseTime, matchWaitWindow)

	wantServe(t, decision, oldest)
}

// Test flow:
//  1. Queue an older waiter that excludes the host and a newer, compatible waiter.
//  2. Match the binding.
//  3. Assert the decision serves the compatible, newer waiter rather than burning the nonce.
func TestMatchConservesNonceForLaterWaiter(t *testing.T) {
	t.Parallel()
	excluding := queuedWaiter(baseTime, "model-a", hostA)
	compatible := queuedWaiter(baseTime.Add(10*time.Millisecond), "model-b")
	binding := HostBinding{Nonce: 4, HostIdx: 0, Participant: hostA}

	decision := match(binding, []*waiter{excluding, compatible}, soleHost, openAvailability(), baseTime, matchWaitWindow)

	wantServe(t, decision, compatible)
}

// Test flow:
//  1. Queue an older waiter that excludes the host and a newer one that also excludes it.
//  2. For each table case of the current clock time, relative to the stale window's expiry, match the binding.
//  3. Assert the decision holds until the oldest waiter's deadline while inside the window.
//  4. Assert the decision serves the excluded waiter, marked despite exclusion, once the window has expired.
func TestMatchHoldsInsideStaleWindow(t *testing.T) {
	t.Parallel()
	oldest := queuedWaiter(baseTime, "model-a", hostA)
	newer := queuedWaiter(baseTime.Add(50*time.Millisecond), "model-b", hostA)
	binding := HostBinding{Nonce: 4, HostIdx: 0, Participant: hostA}

	tests := []struct {
		name     string
		now      time.Time
		wantHold bool
	}{
		{name: "well inside the window holds", now: baseTime.Add(50 * time.Millisecond), wantHold: true},
		{name: "one nanosecond before expiry holds", now: baseTime.Add(matchWaitWindow - time.Nanosecond), wantHold: true},
		{name: "exactly at expiry serves the excluded waiter", now: baseTime.Add(matchWaitWindow)},
		{name: "past expiry serves the excluded waiter", now: baseTime.Add(matchWaitWindow + time.Millisecond)},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := match(binding, []*waiter{oldest, newer}, soleHost, openAvailability(), testCase.now, matchWaitWindow)

			if !testCase.wantHold {
				outcome, served := decision.(serve)
				if !served || !outcome.despiteExclusion {
					t.Fatalf("decision = %T, want the excluded waiter served once the window expired", decision)
				}
				return
			}
			held, ok := decision.(hold)
			if !ok {
				t.Fatalf("decision = %T (%v), want hold", decision, decision)
			}
			if want := baseTime.Add(matchWaitWindow); !held.until.Equal(want) {
				t.Fatalf("hold until = %v, want the oldest waiter's deadline %v", held.until, want)
			}
		})
	}
}

// Test flow:
//  1. Past the stale window's expiry, for each table case build a queue and an optional state-blocked availability.
//  2. Match the binding.
//  3. Assert every case burns with the case's expected `GhostKind`: an exclusion with another host able to serve it burns `ghostExclude` (unlike the sole-host case, served instead), and a diverged state burns `ghostStateDiverged` regardless of the queue.
func TestMatchBurnKindPastStaleWindow(t *testing.T) {
	t.Parallel()
	binding := HostBinding{Nonce: 4, HostIdx: 0, Participant: hostA}
	expired := baseTime.Add(matchWaitWindow)

	tests := []struct {
		name         string
		waiting      []*waiter
		participants []string
		state        func(string) bool
		wantKind     GhostKind
	}{
		{
			name:         "every waiter excludes the host and another can serve it",
			waiting:      []*waiter{queuedWaiter(baseTime, "model-a", hostA)},
			participants: []string{hostA, hostB},
			wantKind:     ghostExclude,
		},
		{
			name:     "the participant's state diverged, whatever is queued",
			waiting:  []*waiter{queuedWaiter(baseTime, "model-a")},
			state:    always(true),
			wantKind: ghostStateDiverged,
		},
		{
			name:     "the participant's state diverged and the queue excludes it too",
			waiting:  []*waiter{queuedWaiter(baseTime, "model-a", hostA)},
			state:    always(true),
			wantKind: ghostStateDiverged,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			availability := openAvailability()
			if testCase.state != nil {
				availability.stateBlocked = testCase.state
			}

			participants := testCase.participants
			if participants == nil {
				participants = soleHost
			}

			decision := match(binding, testCase.waiting, participants, availability, expired, matchWaitWindow)

			wantBurn(t, decision, testCase.wantKind)
		})
	}
}

// Test flow:
//  1. Queue a waiter that excludes `hostA` and match it against two bindings on different slots of that same participant.
//  2. Assert both bindings hold inside the stale window.
//  3. Assert both bindings serve the excluded waiter, marked despite exclusion, once the window has expired.
func TestMatchKeysExclusionByParticipantNotSlot(t *testing.T) {
	t.Parallel()
	excluding := queuedWaiter(baseTime, "model-a", hostA)
	siblingSlots := []HostBinding{
		{Nonce: 4, HostIdx: 0, Participant: hostA},
		{Nonce: 9, HostIdx: 5, Participant: hostA},
	}

	for _, binding := range siblingSlots {
		inside := match(binding, []*waiter{excluding}, soleHost, openAvailability(), baseTime, matchWaitWindow)
		if _, held := inside.(hold); !held {
			t.Fatalf("slot %d gave %T inside the match-wait window, want the nonce held", binding.HostIdx, inside)
		}

		past := match(binding, []*waiter{excluding}, soleHost, openAvailability(), baseTime.Add(matchWaitWindow), matchWaitWindow)
		outcome, served := past.(serve)
		if !served || !outcome.despiteExclusion {
			t.Fatalf("slot %d gave %T past the match-wait window, want the excluded waiter served", binding.HostIdx, past)
		}
	}
}

// Test flow:
//  1. Match a binding against a nil queue, for each table case of no filters, poc required, or state blocked.
//  2. Assert an empty queue with no filters declines, never holds.
//  3. Assert a host-level filter still burns even with nobody queued.
func TestMatchEmptyQueueNeverHolds(t *testing.T) {
	t.Parallel()
	binding := HostBinding{Nonce: 4, HostIdx: 0, Participant: hostA}

	tests := []struct {
		name         string
		availability availability
		wantBurnKind GhostKind
		wantDeclined bool
	}{
		{name: "no filters", availability: openAvailability(), wantDeclined: true},
		{name: "poc required", availability: func() availability {
			availability := openAvailability()
			availability.pocRequired = always(true)
			return availability
		}(), wantBurnKind: ghostPoC},
		{name: "state blocked", availability: func() availability {
			availability := openAvailability()
			availability.stateBlocked = always(true)
			return availability
		}(), wantBurnKind: ghostStateDiverged},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := match(binding, nil, soleHost, testCase.availability, baseTime, matchWaitWindow)

			if testCase.wantDeclined {
				if _, declined := decision.(decline); !declined {
					t.Fatalf("decision = %T (%v), want the nonce declined", decision, decision)
				}
				return
			}
			wantBurn(t, decision, testCase.wantBurnKind)
		})
	}
}

// Test flow:
//  1. Queue two waiters, one excluding `hostA`, into a slice.
//  2. Match a binding against them.
//  3. Assert the queue slice is neither reordered nor resized.
//  4. Assert neither waiter's enqueue time, exclusion set or profile was mutated.
func TestMatchDoesNotMutateItsInputs(t *testing.T) {
	t.Parallel()
	first := queuedWaiter(baseTime, "model-a", hostA)
	second := queuedWaiter(baseTime.Add(time.Millisecond), "model-b")
	waiting := []*waiter{first, second}

	match(HostBinding{Nonce: 4, HostIdx: 0, Participant: hostA}, waiting, soleHost, openAvailability(), baseTime, matchWaitWindow)

	if len(waiting) != 2 || waiting[0] != first || waiting[1] != second {
		t.Fatalf("waiting queue was reordered or resized: %v", waiting)
	}
	if !first.enqueued.Equal(baseTime) || !first.exclude[hostA] || len(first.exclude) != 1 {
		t.Fatalf("first waiter mutated: enqueued=%v exclude=%v", first.enqueued, first.exclude)
	}
	if len(second.exclude) != 0 || second.profile.Model != "model-b" {
		t.Fatalf("second waiter mutated: exclude=%v profile=%+v", second.exclude, second.profile)
	}
}

// Test flow:
//  1. For every combination of poc-required, window-full, state-blocked and ejected predicates, and every queue shape and clock position, match the binding.
//  2. Assert the decision is never nil and always one of the known kinds.
//  3. Assert a serve never crosses an active filter, and an excluded waiter served is marked despite exclusion.
//  4. Assert a hold never crosses a host-level filter, always has a waiter to hold for, and its deadline is the oldest waiter's enqueue time plus the wait window, still ahead of the clock.
//  5. Assert a burn's kind matches whichever host-level filter was active.
func TestMatchIsTotal(t *testing.T) {
	t.Parallel()
	binding := HostBinding{Nonce: 4, HostIdx: 0, Participant: hostA}

	queues := []struct {
		name    string
		waiting []*waiter
	}{
		{name: "empty queue"},
		{name: "compatible waiter", waiting: []*waiter{queuedWaiter(baseTime, "model-a")}},
		{name: "excluding waiter", waiting: []*waiter{queuedWaiter(baseTime, "model-a", hostA)}},
		{name: "two compatible waiters", waiting: []*waiter{
			queuedWaiter(baseTime, "model-a"),
			queuedWaiter(baseTime.Add(time.Millisecond), "model-a"),
		}},
	}
	clocks := []struct {
		name string
		now  time.Time
	}{
		{name: "inside stale window", now: baseTime.Add(matchWaitWindow / 2)},
		{name: "past stale window", now: baseTime.Add(matchWaitWindow)},
	}

	for predicates := range 16 {
		pocRequired := predicates&1 != 0
		windowFull := predicates&2 != 0
		stateBlocked := predicates&4 != 0
		ejected := predicates&8 != 0

		availability := availability{
			pocRequired:  always(pocRequired),
			congested:    windowFullWhen(always(windowFull)),
			ejected:      always(ejected),
			stateBlocked: always(stateBlocked),
		}

		for _, queue := range queues {
			for _, clock := range clocks {
				name := queue.name + "/" + clock.name +
					"/poc=" + boolLabel(pocRequired) + ",window_full=" + boolLabel(windowFull) +
					",state=" + boolLabel(stateBlocked) +
					",ejected=" + boolLabel(ejected)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					decision := match(binding, queue.waiting, soleHost, availability, clock.now, matchWaitWindow)

					if decision == nil {
						t.Fatal("match returned a nil Decision")
					}
					if decisionKind(decision) == "" {
						t.Fatalf("match returned an unknown Decision type %T", decision)
					}
					switch outcome := decision.(type) {
					case serve:
						if pocRequired || windowFull || stateBlocked || ejected {
							t.Fatalf("served through an active filter: %+v", availability)
						}
						if outcome.waiter.exclude[hostA] && !outcome.despiteExclusion {
							t.Fatalf("served an excluded waiter without saying so %+v", outcome.waiter.profile)
						}
					case hold:
						if pocRequired || windowFull {
							t.Fatal("held a nonce past a host-level filter")
						}
						if len(queue.waiting) == 0 {
							t.Fatal("held with no waiter to hold for")
						}
						if want := queue.waiting[0].enqueued.Add(matchWaitWindow); !outcome.until.Equal(want) {
							t.Fatalf("hold until = %v, want %v", outcome.until, want)
						}
						if !clock.now.Before(outcome.until) {
							t.Fatalf("held past the deadline: now=%v until=%v", clock.now, outcome.until)
						}
					case burn:
						switch {
						case pocRequired && outcome.kind != ghostPoC:
							t.Fatalf("burn kind = %s, want %s", outcome.kind.reason(), ghostPoC.reason())
						case !pocRequired && windowFull && outcome.kind != ghostWindowFull:
							t.Fatalf("burn kind = %s, want %s", outcome.kind.reason(), ghostWindowFull.reason())
						}
					}
				})
			}
		}
	}
}

func boolLabel(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// Test flow:
//  1. Queue an abandoned waiter beside a live one and match the binding, asserting the decision serves the live waiter.
//  2. Queue an abandoned excluding waiter beside a live excluding one and match past the abandoned one's deadline, asserting the hold's deadline comes from the live waiter, not the abandoned one.
//  3. Queue only an abandoned excluding waiter and match the binding, asserting the decision declines rather than holding or burning for nobody.
func TestMatchSkipsAbandonedWaiters(t *testing.T) {
	t.Parallel()
	binding := HostBinding{Nonce: 4, HostIdx: 0, Participant: hostA}

	t.Run("an abandoned waiter is never served", func(t *testing.T) {
		t.Parallel()
		abandoned := queuedWaiter(baseTime, "model-a")
		abandoned.abandoned.Store(true)
		live := queuedWaiter(baseTime.Add(time.Millisecond), "model-b")

		decision := match(binding, []*waiter{abandoned, live}, soleHost, openAvailability(), baseTime, matchWaitWindow)

		wantServe(t, decision, live)
	})

	t.Run("the hold deadline comes from the oldest live waiter", func(t *testing.T) {
		t.Parallel()
		abandoned := queuedWaiter(baseTime, "model-a", hostA)
		abandoned.abandoned.Store(true)
		live := queuedWaiter(baseTime.Add(150*time.Millisecond), "model-b", hostA)
		past := baseTime.Add(250 * time.Millisecond)

		decision := match(binding, []*waiter{abandoned, live}, soleHost, openAvailability(), past, matchWaitWindow)

		held, ok := decision.(hold)
		if !ok {
			t.Fatalf("decision = %T (%v), want a hold on the live waiter's deadline", decision, decision)
		}
		if want := live.enqueued.Add(matchWaitWindow); !held.until.Equal(want) {
			t.Fatalf("hold until = %v, want the oldest live waiter's deadline %v", held.until, want)
		}
	})

	t.Run("an entirely abandoned queue neither holds nor burns", func(t *testing.T) {
		t.Parallel()
		abandoned := queuedWaiter(baseTime, "model-a", hostA)
		abandoned.abandoned.Store(true)

		decision := match(binding, []*waiter{abandoned}, soleHost, openAvailability(), baseTime, matchWaitWindow)

		if _, declined := decision.(decline); !declined {
			t.Fatalf("decision = %T (%v), want the nonce declined", decision, decision)
		}
	})
}
