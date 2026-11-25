package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/types"
	"devshard/user"
)

func voteThroughTheRace(t *testing.T, env *testProxyEnv, nonce uint64, params user.InferenceParams, sentAt time.Time) {
	t.Helper()
	_, _, _ = env.proxy.redundancy.runVoteTimeout(t.Context(), &inflight{nonce: nonce, sendTime: sentAt}, params, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
}

func voteThroughTheWarmup(t *testing.T, env *testProxyEnv, nonce uint64, params user.InferenceParams, sentAt time.Time) {
	t.Helper()
	sender := &sessionProbeSender{session: env.session, unserved: map[uint64]unservedProbe{nonce: {prompt: params.Prompt, startedAt: params.StartedAt, sentAt: sentAt}}}
	_ = sender.resolveUnserved(t.Context(), nonce)
}

// Test flow:
//  1. Define a table crossing the two vote paths, the race's due refusal vote and the warmup's vote for an unserved probe, with a nonce left live and a nonce sealed out of the escrow's live state, as the settlement drain does.
//  2. For each case, prepare an inference on a manually armed chain against verifiers that record the payload of every timeout vote they are asked to judge, raise the floor past its refusal deadline, and seal it when the case says so.
//  3. Vote the refusal through the case's path.
//  4. Expect every verifier to receive the payload the host was dispatched with for a live nonce, and no payload for a sealed one.
func TestATimeoutVoteCarriesThePayloadOfTheLiveRecordOnly(t *testing.T) {
	cases := []struct {
		name        string
		vote        func(t *testing.T, env *testProxyEnv, nonce uint64, params user.InferenceParams, sentAt time.Time)
		seal        bool
		wantPayload bool
	}{
		{name: "a race vote for a live nonce carries its dispatched payload", vote: voteThroughTheRace, wantPayload: true},
		{name: "a race vote for a sealed nonce carries none", vote: voteThroughTheRace, seal: true},
		{name: "a warmup vote for a live nonce carries its dispatched payload", vote: voteThroughTheWarmup, wantPayload: true},
		{name: "a warmup vote for a sealed nonce carries none", vote: voteThroughTheWarmup, seal: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			env := setupManualFloorProxy(t, 3, nil, false)
			startedAt := time.Now().Add(-time.Hour)
			params := defaultParams()
			params.StartedAt = startedAt.Unix()
			prepared, err := env.session.PrepareInference(t.Context(), params)
			require.NoError(t, err)
			nonce := prepared.Nonce()
			armed, tracked := env.sm.GetInference(nonce)
			require.True(t, tracked)
			env.raiseFloor(t, armed.StartedAtHeight+100_000)
			if testCase.seal {
				require.NoError(t, env.sm.SealInference(nonce))
				_, live := env.sm.GetInference(nonce)
				require.False(t, live, "the fixture must leave the nonce without a live record")
			}
			var want *host.InferencePayload
			if testCase.wantPayload {
				want = prepared.Payload()
			}

			testCase.vote(t, env, nonce, params, startedAt)

			asked := 0
			for _, verifier := range env.verifiers {
				for _, payload := range verifier.timeoutPayloads() {
					asked++
					require.Equal(t, want, payload)
				}
			}
			require.NotZero(t, asked, "no verifier was asked to judge the vote")
		})
	}
}
