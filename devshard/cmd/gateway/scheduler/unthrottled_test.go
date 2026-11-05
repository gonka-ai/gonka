package scheduler

import "testing"

// laddered builds an `availability` with every gate open unless `closed` shuts one.
func laddered(closed func(*availability)) availability {
	gates := availability{
		pocRequired:  always(false),
		congested:    windowFullWhen(always(false)),
		ejected:      always(false),
		stateBlocked: always(false),
	}
	closed(&gates)
	return gates
}

// Test flow:
//  1. Build a `laddered` availability with a full window and the participant marked unthrottled.
//  2. Ask whether the participant is blocked.
//  3. Assert the reason is `blockNone`, since an unthrottled host is served over a full window.
func TestUnthrottledCrossesAFullWindow(t *testing.T) {
	t.Parallel()
	gates := laddered(func(gates *availability) {
		gates.congested = windowFullWhen(always(true))
		gates.unthrottled = always(true)
	})

	if reason := gates.participantBlocked(hostA); reason != blockNone {
		t.Errorf("reason = %v, want blockNone: an unthrottled host is served over a full window", reason)
	}
}

// Test flow:
//  1. Build a `laddered` availability with the participant ejected and marked unthrottled.
//  2. Ask whether the participant is blocked.
//  3. Assert the reason is `blockNone`.
func TestUnthrottledCrossesEjection(t *testing.T) {
	t.Parallel()
	gates := laddered(func(gates *availability) {
		gates.ejected = always(true)
		gates.unthrottled = always(true)
	})

	if reason := gates.participantBlocked(hostA); reason != blockNone {
		t.Errorf("reason = %v, want blockNone", reason)
	}
}

// Test flow:
//  1. Build a `laddered` availability where `hostB` is allowlisted and `hostA` is unthrottled.
//  2. Ask whether `hostA` is blocked and assert the reason is `blockNone`.
//  3. Ask whether `hostB` is blocked and assert the reason is `blockNone`.
//  4. Ask whether an unlisted stranger is blocked and assert the reason is `blockNotAllowed`.
func TestUnthrottledIsAdmittedOutsideTheAllowlist(t *testing.T) {
	t.Parallel()
	gates := laddered(func(gates *availability) {
		gates.notAllowed = refusedByAllowlist([]string{hostB}, []string{hostA})
		gates.unthrottled = waivesThrottling([]string{hostA})
	})

	if reason := gates.participantBlocked(hostA); reason != blockNone {
		t.Errorf("unthrottled host: reason = %v, want blockNone", reason)
	}
	if reason := gates.participantBlocked(hostB); reason != blockNone {
		t.Errorf("allowlisted host: reason = %v, want blockNone", reason)
	}
	if reason := gates.participantBlocked("stranger"); reason != blockNotAllowed {
		t.Errorf("stranger: reason = %v, want blockNotAllowed", reason)
	}
}

// Test flow:
//  1. For each table case (cut off, owes proof-of-compute, escrow state diverged), close that one gate on a `laddered` availability with the participant marked unthrottled.
//  2. Ask whether the participant is blocked.
//  3. Assert the reason matches the case's expected block, since unthrottled does not cross these gates.
func TestUnthrottledStopsAtTheGatesThatProtectTheNonce(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		close func(*availability)
		want  blockReason
	}{
		"cut off": {
			close: func(gates *availability) {
				gates.congested = func(string) blockReason { return blockCutOff }
			},
			want: blockCutOff,
		},
		"owes proof-of-compute": {
			close: func(gates *availability) { gates.pocRequired = always(true) },
			want:  blockPoCRequired,
		},
		"escrow state diverged": {
			close: func(gates *availability) { gates.stateBlocked = always(true) },
			want:  blockStateDiverged,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			gates := laddered(func(gates *availability) {
				testCase.close(gates)
				gates.unthrottled = always(true)
			})

			if reason := gates.participantBlocked(hostA); reason != testCase.want {
				t.Errorf("reason = %v, want %v", reason, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build a reachability check with a fake allowlist naming nobody and `hostA` marked unthrottled.
//  2. Assert an escrow whose slots and participants are `hostA` is reachable.
//  3. Assert an escrow held by a stranger is not.
func TestEscrowHoldingOnlyAnUnthrottledParticipantIsReachable(t *testing.T) {
	t.Parallel()
	reachable := reachableByAllowlist([]string{"nobody-holds-this"}, []string{hostA})

	if !reachable(Escrow{Session: &fakeSession{slots: soleHost, participants: soleHost}}) {
		t.Error("an escrow whose group holds an unthrottled participant must stay reachable")
	}
	stranger := []string{"stranger"}
	if reachable(Escrow{Session: &fakeSession{slots: stranger, participants: stranger}}) {
		t.Error("an escrow holding neither an allowed nor an unthrottled participant must stay unreachable")
	}
}

// Test flow:
//  1. Build a harness with one host whose window is always full but who is marked unthrottled.
//  2. Submit a stale request and await its reply.
//  3. Assert the reply carries an assignment rather than an error.
//  4. Assert no ghost burns were recorded.
func TestUnthrottledServesOverAFullWindowWithoutBurning(t *testing.T) {
	test := newHarness(t, harnessConfig{
		slots:       soleHost,
		windowFull:  always(true),
		unthrottled: always(true),
	})

	queued := test.submit(t, test.clock.Now().Add(-2*matchWaitWindow))

	result := awaitReply(t, queued)
	if result.err != nil {
		t.Fatalf("err = %v, want an assignment over the full window", result.err)
	}
	test.dispatcher.stop()

	if burns := test.observer.burns(); len(burns) != 0 {
		t.Errorf("ghost burns = %v, want none", burns)
	}
}
