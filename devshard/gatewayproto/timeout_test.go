package gatewayproto

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"devshard/host"
	"devshard/user"
)

type timeoutRecorder struct {
	result       user.TimeoutResult
	err          error
	normal, miss int
	payload      *host.InferencePayload
	finish, body []byte
}

func (h *timeoutRecorder) HandleTimeout(_ context.Context, _ uint64, _ time.Time, payload *host.InferencePayload) (user.TimeoutResult, error) {
	h.normal++
	h.payload = payload
	return h.result, h.err
}
func (h *timeoutRecorder) HandleErrorMiss(_ context.Context, _ uint64, finish, body []byte) (user.TimeoutResult, error) {
	h.miss++
	h.finish = finish
	h.body = body
	return h.result, h.err
}

func TestProtocolTimeoutPreservesFullResultAlongsideError(t *testing.T) {
	failure := errors.New("delivery failed after apply")
	for _, err := range []error{nil, failure, user.ErrInferenceMissed} {
		result := user.TimeoutResult{Reason: "error", Outcome: "applied", DetailReason: "detail", Applied: true, Votes: 3, Accepted: true, ResponseHash: []byte("hash"), VerifyRejects: []string{"cause"}}
		handler := &timeoutRecorder{result: result, err: err}
		claim := &MissClaim{FinishTx: []byte("finish"), ResponsePayload: []byte("response")}
		got, gotErr := HandleProtocolTimeout(context.Background(), handler, 9, time.Time{}, nil, claim)
		if !reflect.DeepEqual(got, result) || gotErr != err {
			t.Fatalf("result/error changed: %+v %v", got, gotErr)
		}
		if handler.miss != 1 || handler.normal != 0 || string(handler.finish) != "finish" || string(handler.body) != "response" {
			t.Fatal("claim was not passed through")
		}
	}
}

func TestProtocolTimeoutFallsBackOnlyWithoutSignedFinish(t *testing.T) {
	payload := &host.InferencePayload{Model: "model", Prompt: []byte("prompt")}
	for _, claim := range []*MissClaim{nil, {ResponsePayload: []byte("unsigned")}} {
		handler := &timeoutRecorder{}
		_, err := HandleProtocolTimeout(context.Background(), handler, 2, time.Unix(10, 0), payload, claim)
		if err != nil || handler.normal != 1 || handler.miss != 0 || handler.payload != payload {
			t.Fatal("unsigned/missing claim did not keep the normal timeout path")
		}
	}
}
