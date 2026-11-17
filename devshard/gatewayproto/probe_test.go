package gatewayproto

import (
	"errors"
	"testing"

	"devshard/host"
	"devshard/user"
)

func TestProbeReplyKeepsSendErrorAndAcknowledgementPolicy(t *testing.T) {
	sendFailure := errors.New("send failed")
	applyFailure := errors.New("apply failed")
	for _, tc := range []struct {
		name                       string
		sendErr, applyErr          error
		receipt, notified, wantAck bool
		wantApply                  int
	}{
		{"receipt in failed reply", sendFailure, nil, true, false, false, 0},
		{"receipt callback before failed send", sendFailure, nil, false, true, true, 0},
		{"executor receipt", nil, nil, true, false, true, 1},
		{"receipt before apply error", nil, applyFailure, true, false, true, 1},
		{"no executor receipt", nil, nil, false, false, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := &host.HostResponse{}
			if tc.receipt {
				reply.Receipt = []byte("executor receipt")
			}
			applied := 0
			ack, err := ApplyProbeResponse(reply, tc.sendErr, tc.notified, func() error { applied++; return tc.applyErr })
			wantErr := tc.sendErr
			if wantErr == nil {
				wantErr = tc.applyErr
			}
			if ack != tc.wantAck || err != wantErr || applied != tc.wantApply {
				t.Fatalf("ack=%t error=%v apply=%d", ack, err, applied)
			}
		})
	}
}

func TestProbeParamsPreserveEscrowModelAndTime(t *testing.T) {
	original := user.InferenceParams{Model: "escrow-model", StartedAt: 123, Prompt: []byte("user request"), InputLength: 99, MaxTokens: 100}
	prompt := []byte(`{"messages":[{"role":"user","content":"."}],"max_tokens":7}`)
	got := ProbeParams(original, prompt, 7)
	if got.Model != original.Model || got.StartedAt != original.StartedAt || string(got.Prompt) != string(prompt) || got.InputLength != uint64(len(prompt)) || got.MaxTokens != 7 {
		t.Fatalf("probe params = %+v", got)
	}
	if original.MaxTokens != 100 || string(original.Prompt) != "user request" {
		t.Fatal("input params were mutated")
	}
}
