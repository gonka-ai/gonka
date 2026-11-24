package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/config"
	"devshard/host"
	"devshard/user"
)

// Test flow:
//  1. Send a client chat with default, joined and separate operator schedules.
//  2. Inspect the inference parameters passed to the race for each request.
//  3. Assert each request carries an independent copy of the operator's schedule.
func TestChatRequestsCarryTheConfiguredFrameSchedule(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		schedule config.ChatFrames
	}{
		{name: "default", schedule: config.Defaults().ChatFrames},
		{name: "joined", schedule: config.ChatFrames{Events: 10}},
		{name: "separate", schedule: config.ChatFrames{Events: 64, Separate: true}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t, func(configuration *config.Config) {
				configuration.ChatFrames = testCase.schedule
				grantAccess(configuration, map[string]string{"qwen": config.ModelAccessOpen})
			})
			response := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)
			require.Equal(t, http.StatusOK, response.Code)
			params := live.inference.raced.Load().Params.(user.InferenceParams)
			require.Equal(t, &host.ChatFrameSchedule{Every: uint32(testCase.schedule.Events), Separate: testCase.schedule.Separate}, params.ChatFrames)
			params.ChatFrames.Every = 1
			response = live.request(t, http.MethodPost, "/v1/chat/completions", strings.Replace(chatBody, `"hi"`, `"another question"`, 1), nil)
			require.Equal(t, http.StatusOK, response.Code)
			nextParams := live.inference.raced.Load().Params.(user.InferenceParams)
			require.NotSame(t, params.ChatFrames, nextParams.ChatFrames)
			require.Equal(t, &host.ChatFrameSchedule{Every: uint32(testCase.schedule.Events), Separate: testCase.schedule.Separate}, nextParams.ChatFrames)
		})
	}
}

type recordedChatFrames struct {
	escrowID    string
	participant string
	nonce       uint64
	checks      []host.ChatFrameCheck
}

func (r *recordedChatFrames) ChatFramesChecked(escrowID, participant string, nonce uint64, check host.ChatFrameCheck) {
	r.escrowID, r.participant, r.nonce = escrowID, participant, nonce
	r.checks = append(r.checks, check)
}

// Test flow:
//  1. Prepare a real session nonce and return valid, invalid and unsigned frame observations beside a successful or failed send.
//  2. Dispatch each response through the gateway adapter with a recording narrator.
//  3. Assert observations keep their nonce and host, responses are applied, and verification never changes the send error.
func TestDispatchReportsFrameChecksWithoutChangingTheResponse(t *testing.T) {
	upstreamFailure := errors.New("upstream disconnected")
	for _, testCase := range []struct {
		name  string
		check host.ChatFrameCheck
		err   error
	}{
		{name: "signed", check: host.ChatFrameCheck{Frames: 8, Signed: 7}},
		{name: "invalid", check: host.ChatFrameCheck{Frames: 8, Invalid: "the signer is not the executor"}},
		{name: "unsigned", check: host.ChatFrameCheck{Frames: 8}},
		{name: "partial response", check: host.ChatFrameCheck{Frames: 2, Invalid: "incomplete prefix"}, err: upstreamFailure},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			session, _ := newLiveSession(t)
			prepared := prepareNonce(t, session)
			reply := &host.HostResponse{ChatFrames: testCase.check}
			recorded := &recordingSession{reply: reply, sendErr: testCase.err}
			narrator := &recordedChatFrames{}
			target := escrowTarget{session: recorded, escrowID: liveEscrowID, chatFrames: narrator}
			response, err := target.Send(t.Context(), prepared, io.Discard, nil)
			require.ErrorIs(t, err, testCase.err)
			require.Same(t, reply, response.(hostReply).reply)
			require.Same(t, reply, recorded.applied.reply)
			require.Equal(t, []host.ChatFrameCheck{testCase.check}, narrator.checks)
			require.Equal(t, liveEscrowID, narrator.escrowID)
			require.Equal(t, prepared.Nonce(), narrator.nonce)
			require.Equal(t, recorded.HostLabel(prepared.HostIdx()), narrator.participant)
			response.(hostReply).ReleaseFinish()
		})
	}
}
