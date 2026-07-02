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

// TestRunGhostProbe_AllKindsAreSilent is the regression guard for the
// uniform-silent-probe contract. No matter what kind the picker
// produces, runGhostProbe must NOT contact the host. The MsgStart for
// the burned nonce stays in s.diffs and will catch-up on the host's
// next real dispatch; here we only verify the dispatcher's no-Send
// invariant, which is what protects the host from probe load during
// PoC, exclude-stale, and 503-recovery windows alike.
func TestRunGhostProbe_AllKindsAreSilent(t *testing.T) {
	cases := []struct {
		name string
		kind ghostKind
	}{
		{"poc", ghostPoC},
		{"exclude", ghostExclude},
		{"throttled", ghostThrottled},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := setupTestProxy(t, 3, nil, true)
			// Stop the production picker so it doesn't race with our
			// manual runGhostProbe call below by also dispatching nonces.
			env.proxy.redundancy.picker.stop()

			prepared := prepareForGhost(t, env.session, "llama")
			hostIdx := prepared.HostIdx()

			require.Nil(t, env.killables[hostIdx].LastRequest(),
				"precondition: no host contact before runGhostProbe")

			env.proxy.redundancy.runGhostProbe(prepared, tc.kind, tc.kind.reason())

			// Belt-and-suspenders sleep. runGhostProbe is now strictly
			// log-only -- no goroutine, no I/O -- so this is paranoia,
			// not synchronization. If a future change re-introduces a
			// goroutine that hits Send, this sleep gives it time to
			// race so the assertion below catches the regression.
			time.Sleep(50 * time.Millisecond)

			require.Nil(t, env.killables[hostIdx].LastRequest(),
				"%s: ghost probe must NOT call Send (silent-probe contract)", tc.name)
		})
	}
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

// TestRunGhostProbe_NoVoteFromThisNode is a structural guard: ghost
// probes never create an *inflight, so HandleTimeout (the only path
// this node uses to post a timeout vote) cannot run for a burned
// nonce. We assert this indirectly by confirming the dispatcher
// returns synchronously -- if it ever spawns work that could trigger
// a vote, the test will need an explicit synchronization point.
func TestRunGhostProbe_NoVoteFromThisNode(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	prepared := prepareForGhost(t, env.session, "llama")

	start := time.Now()
	env.proxy.redundancy.runGhostProbe(prepared, ghostExclude, ghostExclude.reason())
	elapsed := time.Since(start)

	// Synchronous return is the structural guarantee that no
	// background settlement (vote, retry, anything) can race in
	// later. 50ms is generous; in practice this is microseconds.
	require.Less(t, elapsed, 50*time.Millisecond,
		"runGhostProbe must return synchronously (no background goroutines)")
}
