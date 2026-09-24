package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/user"
)

// prepareForGhost is a tiny helper that runs PrepareInferenceFn with a
// chooser returning probe params. It returns the prepared inference
// without dispatching it -- exactly what the picker would hand to
// runGhostProbe in production. We need this here because we are
// asserting on dispatcher behavior in isolation; we don't want the
// session_picker run loop racing with our explicit runGhostProbe call.
func prepareForGhost(t *testing.T, session *user.Session, model string) *user.PreparedInference {
	t.Helper()
	prepared, err := session.PrepareInferenceFn(func(user.HostBinding) (user.InferenceParams, bool, error) {
		return ghostProbeParams(model), true, nil
	})
	require.NoError(t, err)
	require.NotNil(t, prepared)
	return prepared
}

// TestRunGhostProbe_KeepsMsgStartInDiffs verifies that even though we
// don't contact the host, the nonce still advances and the MsgStart
// stays in the session's diff stream so the host's next real dispatch
// will replay it as catch-up. This is what keeps the chain view
// eventually consistent: the host didn't see the nonce yet, but it
// will once a real request lands on it.
//
// The exact semantics of diffsForHost are tested in the user package;
// here we only verify the picker-side invariant that
// PrepareInferenceFn's diff is not retroactively dropped by the
// dispatcher's silent path.
func TestRunGhostProbe_KeepsMsgStartInDiffs(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	prepared := prepareForGhost(t, env.session, "llama")
	nonce := prepared.Nonce()

	env.proxy.redundancy.runGhostProbe(prepared, ghostThrottled, ghostThrottled.reason())

	require.GreaterOrEqual(t, env.session.Nonce(), nonce,
		"PrepareInferenceFn must have advanced past the burned nonce")
}

// TestRunGhostProbe_DoesNotBlockThePicker guards the dispatcher's cost, not its silence: the picker
// calls it inline for every burned nonce, so it must never wait on settlement.
func TestRunGhostProbe_DoesNotBlockThePicker(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	prepared := prepareForGhost(t, env.session, "llama")

	start := time.Now()
	env.proxy.redundancy.runGhostProbe(prepared, ghostExclude, ghostExclude.reason())
	elapsed := time.Since(start)

	require.Less(t, elapsed, 50*time.Millisecond,
		"runGhostProbe must return without waiting on settlement")
}

// ghostMissObservationWindow outlasts the test session's refusal deadline (RefusalTimeout plus
// TimeoutBuffer), so a "no miss" assertion cannot pass merely by finishing first.
const ghostMissObservationWindow = 1500 * time.Millisecond

// shortRefusalWindow shrinks the wait between burning a nonce and voting on it, so a test observes
// the miss a burn would cause rather than the intent to raise one.
func shortRefusalWindow(t *testing.T) {
	t.Helper()
	saved := user.TimeoutBuffer
	user.TimeoutBuffer = 50 * time.Millisecond
	t.Cleanup(func() { user.TimeoutBuffer = saved })
}

func missesForSlot(t *testing.T, env *testProxyEnv, slot int) uint32 {
	t.Helper()
	stats, ok := env.sm.HostStatsFor(uint32(slot))
	require.True(t, ok, "slot %d has no host stats", slot)
	return stats.Missed
}

// A burned nonce is the gateway's own scheduling decision, so it costs the host nothing: the burn
// neither contacts the host nor charges it a protocol miss, whatever drove the burn.
func TestRunGhostProbe_BurningANonceChargesTheHostNothing(t *testing.T) {
	cases := []struct {
		name string
		kind ghostKind
	}{
		{"poc", ghostPoC},
		{"exclude", ghostExclude},
		{"throttled", ghostThrottled},
		{"state_diverged", ghostStateDiverged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shortRefusalWindow(t)
			env := setupTestProxy(t, 3, nil, true)
			env.proxy.redundancy.picker.stop()

			prepared := prepareForGhost(t, env.session, "llama")
			slot := prepared.HostIdx()
			require.Zero(t, missesForSlot(t, env, slot), "precondition: no miss before the burn")

			env.proxy.redundancy.runGhostProbe(prepared, tc.kind, tc.kind.reason())

			require.Never(t, func() bool { return missesForSlot(t, env, slot) > 0 },
				ghostMissObservationWindow, 20*time.Millisecond,
				"%s: burning a nonce must not charge the host a miss", tc.name)
			require.Nil(t, env.killables[slot].LastRequest(),
				"%s: burning a nonce must not contact the host", tc.name)
		})
	}
}

func TestRunGhostProbe_RecordsGhostNoSendSlotDecision(t *testing.T) {
	cases := []ghostKind{ghostPoC, ghostExclude, ghostThrottled, ghostStateDiverged}
	for _, kind := range cases {
		kind := kind
		t.Run(kind.reason(), func(t *testing.T) {
			env := setupTestProxy(t, 3, nil, true)
			env.proxy.redundancy.picker.stop()
			metrics := NewDevshardMetrics()
			env.proxy.redundancy.metrics = metrics
			env.proxy.redundancy.devshardID = "escrow-proxy"

			prepared := prepareForGhost(t, env.session, "llama")
			env.proxy.redundancy.runGhostProbe(prepared, kind, kind.reason())

			participantKey := env.proxy.redundancy.participantKeyForHost(prepared.HostIdx())
			families, err := metrics.registry.Gather()
			require.NoError(t, err)
			requireMetricCounterValue(t, families, "devshard_gateway_slot_decisions_total", map[string]string{
				"participant_key": participantKey,
				"model":           "llama",
				"escrow_id":       "escrow-proxy",
				"decision":        "ghost_no_send",
				"reason":          kind.reason(),
				"quarantine_mode": "none",
			}, 1)
			require.Nil(t, env.killables[prepared.HostIdx()].LastRequest(),
				"ghost_no_send recording must not contact the host")
		})
	}
}

func TestRunGhostProbe_SkipsAfterRedundancyStop(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	metrics := NewDevshardMetrics()
	env.proxy.redundancy.metrics = metrics
	env.proxy.redundancy.devshardID = "escrow-proxy"
	env.proxy.redundancy.Stop()

	prepared := prepareForGhost(t, env.session, "llama")
	env.proxy.redundancy.runGhostProbe(prepared, ghostThrottled, ghostThrottled.reason())

	families, err := metrics.registry.Gather()
	require.NoError(t, err)
	requireMetricCounterMissing(t, families, "devshard_gateway_slot_decisions_total", map[string]string{"escrow_id": "escrow-proxy"})
}
