package api

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"devshard/cmd/gateway/filters"
)

// Test flow:
//  1. Create a non-streaming `clientStream` and write 1MB chunks to it in a loop, past `maxBufferedResponseBytes`.
//  2. Assert the loop eventually fails with `filters.ErrStreamCarryOverflow`.
//  3. Assert the bytes written before the refusal never exceeded `maxBufferedResponseBytes`.
func TestAnUnterminatedEventIsBoundedInMemory(t *testing.T) {
	stream := newClientStream(httptest.NewRecorder(), "req-1", false, true, filters.LogprobIntent{}, nil)
	chunk := bytes.Repeat([]byte("x"), 1<<20)

	written := 0
	var failure error
	for written <= maxBufferedResponseBytes+len(chunk) {
		n, err := stream.Write(chunk)
		if err != nil {
			failure = err
			break
		}
		written += n
	}

	if failure == nil {
		t.Fatalf("wrote %d bytes with no limit, want a refusal past %d", written, maxBufferedResponseBytes)
	}
	if !errors.Is(failure, filters.ErrStreamCarryOverflow) {
		t.Fatalf("error = %v, want the carry bound", failure)
	}
	if written > maxBufferedResponseBytes {
		t.Fatalf("held %d bytes, want no more than %d", written, maxBufferedResponseBytes)
	}
}

// Test flow:
//  1. Write 200 SSE chunks, each carrying content plus logprobs, into a non-streaming `clientStream`.
//  2. Read the folder's held byte count.
//  3. Assert it stays well below a quarter of the raw bytes written, so the discarded logprobs are not retained.
func TestAFoldedReplyHoldsTheAnswerRatherThanTheStream(t *testing.T) {
	stream := newClientStream(httptest.NewRecorder(), "req-1", false, true, filters.LogprobIntent{}, nil)

	raw := 0
	for token := range 200 {
		event := fmt.Appendf(nil, "data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"%d \"},\"logprobs\":{\"content\":[{\"token\":\"%d\",\"logprob\":-0.5,\"top_logprobs\":[{\"token\":\"a\",\"logprob\":-1.5},{\"token\":\"b\",\"logprob\":-2.5},{\"token\":\"c\",\"logprob\":-3.5}]}]}}]}\n\n", token, token)
		if _, err := stream.Write(event); err != nil {
			t.Fatalf("Write(): %v", err)
		}
		raw += len(event)
	}

	held := stream.folder.Held()
	if held >= int64(raw)/4 {
		t.Errorf("holding %d bytes of a %d-byte stream: the logprobs this client never asked for are being kept", held, raw)
	}
}

// Test flow:
//  1. Write one small SSE chunk into a non-streaming `clientStream` and close it.
//  2. Assert the recorded body contains the assembled reply's content.
func TestNonStreamingRepliesUnderTheBoundAreKept(t *testing.T) {
	recorder := httptest.NewRecorder()
	stream := newClientStream(recorder, "req-1", false, true, filters.LogprobIntent{}, nil)

	if _, err := stream.Write([]byte(`data: {"choices":[{"message":{"content":"ok"}}]}` + "\n\n")); err != nil {
		t.Fatalf("Write(): %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	if !strings.Contains(recorder.Body.String(), `"content":"ok"`) {
		t.Fatalf("body = %q, want the assembled reply", recorder.Body.String())
	}
}

// Test flow:
//  1. Create a non-streaming `clientStream` backed by a `BufferBudget` sized to 1 byte.
//  2. Write an oversized SSE chunk to it.
//  3. Assert both the write and the close fail with `ErrResponseBufferFull`.
//  4. Assert the recorded status is 503 and the oversized content never reached the body.
func TestAReplyPastTheBufferBudgetIsRefused(t *testing.T) {
	recorder := httptest.NewRecorder()
	stream := newClientStream(recorder, "req-1", false, true, filters.LogprobIntent{}, NewBufferBudget(1))

	oversized := `data: {"choices":[{"delta":{"content":"` + strings.Repeat("x", 512<<10) + `"}}]}` + "\n\n"
	if _, err := stream.Write([]byte(oversized)); !errors.Is(err, ErrResponseBufferFull) {
		t.Fatalf("Write() = %v, want %v", err, ErrResponseBufferFull)
	}
	if err := stream.Close(); !errors.Is(err, ErrResponseBufferFull) {
		t.Fatalf("Close() = %v, want %v", err, ErrResponseBufferFull)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "xxxx") {
		t.Error("the refused reply was served anyway")
	}
}

// Test flow:
//  1. Write one SSE content chunk into a streaming `clientStream` and close it.
//  2. Read the delivered byte count and terminated flag.
//  3. Assert delivered bytes match what the recorder actually received.
//  4. Assert the stream reports terminated.
func TestAStreamCountsWhatReachedTheClient(t *testing.T) {
	recorder := httptest.NewRecorder()
	stream := newClientStream(recorder, "req-1", true, true, filters.LogprobIntent{}, nil)

	if _, err := stream.Write([]byte(`data: {"choices":[{"delta":{"content":"ok"}}]}` + "\n\n")); err != nil {
		t.Fatalf("Write(): %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	written, terminated := stream.delivered()
	if want := int64(recorder.Body.Len()); written != want {
		t.Fatalf("delivered %d bytes, but %d reached the recorder", written, want)
	}
	if !terminated {
		t.Fatal("the terminator never went out, so a client would wait out its own timeout")
	}
}

// Test flow:
//  1. Write one SSE content chunk into a non-streaming `clientStream` and close it.
//  2. Assert delivered bytes match what the recorder received and are non-zero.
func TestANonStreamingReplyCountsItsBody(t *testing.T) {
	recorder := httptest.NewRecorder()
	stream := newClientStream(recorder, "req-1", false, true, filters.LogprobIntent{}, nil)

	if _, err := stream.Write([]byte(`data: {"choices":[{"message":{"content":"ok"}}]}` + "\n\n")); err != nil {
		t.Fatalf("Write(): %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	written, _ := stream.delivered()
	if want := int64(recorder.Body.Len()); written != want {
		t.Fatalf("delivered %d bytes, but %d reached the recorder", written, want)
	}
	if written == 0 {
		t.Fatal("a served reply reported nothing delivered")
	}
}

// Test flow:
//  1. Define a table of raw SSE event bodies, varying across a stream that carried no payload and a genuine host answer.
//  2. For each case, write the events into a non-streaming `clientStream` and close it.
//  3. Assert the recorded status matches the case's expectation.
//  4. For the no-payload case, assert the body equals `filters.NoResponseDataBody`.
func TestABodyTheAssemblerCouldNotFoldIsNotServedAsSuccess(t *testing.T) {
	tests := []struct {
		name       string
		events     string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "a stream that carried no payload",
			events:     "\n\n",
			wantStatus: 502,
			wantBody:   string(filters.NoResponseDataBody),
		},
		{
			name:       "an answer the host actually produced",
			events:     "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
			wantStatus: 200,
		},
	}
	for _, testCase := range tests {
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
				t.Errorf("status = %d, want %d for body %s", recorder.Code, testCase.wantStatus, recorder.Body)
			}
			if testCase.wantBody != "" && strings.TrimSpace(recorder.Body.String()) != testCase.wantBody {
				t.Errorf("body = %s, want %s", recorder.Body, testCase.wantBody)
			}
		})
	}
}
