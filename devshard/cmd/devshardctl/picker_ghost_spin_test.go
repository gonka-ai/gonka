package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Host 1 is throttled and hosts 0 and 2 are catching up: no host can take the
// request, so the picker must not burn nonces as ghosts while it waits.
func TestPicker_CatchingUpHosts_NoGhostSpinWhileOtherHostThrottled(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	throttledKey := env.session.HostParticipantKey(1)
	ghost := &fakeGhost{}
	p := newSessionPicker(env.session, "llama", ghost.dispatch,
		func(key string) bool { return key == throttledKey }, nil)
	p.catchingUp = func(hostIdx int) bool { return hostIdx == 0 || hostIdx == 2 }
	p.start()
	t.Cleanup(p.stop)

	start := env.session.Nonce()
	req := defaultPickerRequest()
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.NoError(t, res.err)
	require.False(t, res.isProbe)
	require.NotEqual(t, 1, res.prepared.HostIdx(), "the throttled host gets no real request")
	require.Less(t, env.session.Nonce()-start, uint64(10), "nonces burned while no host could serve")
}

// Host 1 is ready but the request already tried it, and hosts 0 and 2 are
// catching up: the request must wait for a catch-up, not spin nonces.
func TestPicker_CatchingUpHosts_NoGhostSpinWhileReadyHostExcluded(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	ghost := &fakeGhost{}
	p := newSessionPicker(env.session, "llama", ghost.dispatch, nil, nil)
	p.catchingUp = func(hostIdx int) bool { return hostIdx == 0 || hostIdx == 2 }
	p.start()
	t.Cleanup(p.stop)

	start := env.session.Nonce()
	req := defaultPickerRequest()
	req.excludeParticipants = map[string]bool{env.session.HostParticipantKey(1): true}
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.NoError(t, res.err)
	require.False(t, res.isProbe)
	require.NotEqual(t, 1, res.prepared.HostIdx(), "the excluded host gets no real request")
	require.Less(t, env.session.Nonce()-start, uint64(10), "nonces burned while no host could serve")
}
