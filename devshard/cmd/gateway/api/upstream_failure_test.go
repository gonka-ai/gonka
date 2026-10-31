package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"devshard/cmd/gateway/filters"
)

// Test flow:
//  1. Define a table of buffered SSE event bodies, varying across: an error object beside interrupted content, an error the host sent as a bare string, a request error the host names with its own status, the flat error shape vLLM answers with, and a genuine completed answer.
//  2. For each case, write the events through a buffered `clientStream` and close it.
//  3. Assert the recorded HTTP status matches the case's expectation, so an assembled failure is never served as a success.
func TestAnAssembledFailureIsNotServedAsASuccess(t *testing.T) {
	testCases := []struct {
		name       string
		events     string
		wantStatus int
	}{
		{
			name:       "an error object beside the content it interrupted",
			events:     `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n" + `data: {"error":{"message":"boom"}}` + "\n\ndata: [DONE]\n\n",
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "an error the host sent as a bare string",
			events:     `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n" + `data: {"error":"boom"}` + "\n\ndata: [DONE]\n\n",
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "a request error the host names with its own status",
			events:     `data: {"error":{"message":"context too long","code":400,"type":"BadRequestError"}}` + "\n\ndata: [DONE]\n\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "the flat shape vLLM answers with",
			events:     `data: {"object":"error","message":"boom","code":503}` + "\n\ndata: [DONE]\n\n",
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "an answer the host actually produced",
			events:     `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n",
			wantStatus: http.StatusOK,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			stream := newClientStream(recorder, "req-1", false, true, filters.LogprobIntent{}, nil)
			if _, err := stream.Write([]byte(testCase.events)); err != nil {
				t.Fatalf("Write(): %v", err)
			}

			if err := stream.Close(); err != nil {
				t.Fatalf("Close(): %v", err)
			}

			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d for body %s", recorder.Code, testCase.wantStatus, recorder.Body)
			}
		})
	}
}

// Test flow:
//  1. Write a streaming reply that already contains content and the host's [DONE] terminator.
//  2. Fail the stream after that point.
//  3. Assert an error appears in the body before the terminator, so a client reading up to [DONE] still sees the failure.
//  4. Assert the terminator appears exactly once.
func TestAFailureAfterTheHostFinishedStillReachesTheClient(t *testing.T) {
	recorder := httptest.NewRecorder()
	stream := newClientStream(recorder, "req-1", true, true, filters.LogprobIntent{}, nil)
	if _, err := stream.Write([]byte(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\ndata: [DONE]\n\n")); err != nil {
		t.Fatalf("Write(): %v", err)
	}

	if err := stream.Fail(errors.New("winner failed after streaming started")); err != nil {
		t.Fatalf("Fail(): %v", err)
	}

	body := recorder.Body.String()
	failure := strings.Index(body, `"error"`)
	terminator := strings.Index(body, "data: [DONE]")
	switch {
	case failure < 0:
		t.Fatalf("no failure reached the client: %s", body)
	case terminator < 0:
		t.Fatalf("no terminator reached the client, so it waits out its own timeout: %s", body)
	case terminator < failure:
		t.Fatalf("the failure was written behind the terminator, where no client reads it: %s", body)
	}
	if strings.Count(body, "data: [DONE]") != 1 {
		t.Fatalf("the terminator was written %d times: %s", strings.Count(body, "data: [DONE]"), body)
	}
}

// Test flow:
//  1. Write a streaming reply that ends with the host's [DONE] terminator, then close the stream cleanly.
//  2. Assert the terminator appears exactly once in the body.
//  3. Assert the body ends on that terminator.
func TestACleanStreamEndsWithOneTerminator(t *testing.T) {
	recorder := httptest.NewRecorder()
	stream := newClientStream(recorder, "req-1", true, true, filters.LogprobIntent{}, nil)
	if _, err := stream.Write([]byte(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\ndata: [DONE]\n\n")); err != nil {
		t.Fatalf("Write(): %v", err)
	}

	if err := stream.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	body := recorder.Body.String()
	if got := strings.Count(body, "data: [DONE]"); got != 1 {
		t.Fatalf("terminators = %d, want exactly one: %s", got, body)
	}
	if !strings.HasSuffix(strings.TrimRight(body, "\n"), "data: [DONE]") {
		t.Fatalf("the stream does not end on its terminator: %s", body)
	}
}
