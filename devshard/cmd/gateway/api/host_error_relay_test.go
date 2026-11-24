package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"devshard/cmd/gateway/engine"
)

const hostWrittenMessage = "Your API key was revoked for abuse. Sign in at https://gateway-billing.example to restore access."

func failRaceWithHostError(live *harness, status int, message string) {
	live.inference.reply = ""
	live.inference.outcome = engine.RaceOutcome{EscrowID: "7"}
	live.inference.err = &engine.HostApplicationError{Code: strconv.Itoa(status), Type: "host_error", Message: message}
}

func decodeErrorMessage(t *testing.T, body []byte) string {
	t.Helper()
	var envelope errorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("json.Unmarshal(%s) = %v", body, err)
	}
	return envelope.Error.Message
}

// Test flow:
//  1. For each code a host may put in its error object, start a harness whose race fails before its first byte with a `HostApplicationError` carrying that code and a host-written message.
//  2. Send one non-streaming chat completion.
//  3. Assert the client receives the gateway's status for that code and the gateway's message, with the host's text only after a request fault's message.
func TestTheGatewayWritesTheStatusAndMessageForAHostError(t *testing.T) {
	testCases := []struct {
		hostStatus  int
		wantStatus  int
		wantMessage string
	}{
		{hostStatus: http.StatusBadRequest, wantStatus: http.StatusBadRequest, wantMessage: hostRejectedRequestMessage + hostExcerptSeparator + hostWrittenMessage},
		{hostStatus: http.StatusUnprocessableEntity, wantStatus: http.StatusUnprocessableEntity, wantMessage: hostRejectedRequestMessage + hostExcerptSeparator + hostWrittenMessage},
		{hostStatus: http.StatusUnauthorized, wantStatus: http.StatusBadGateway, wantMessage: hostFailedMessage},
		{hostStatus: http.StatusForbidden, wantStatus: http.StatusBadGateway, wantMessage: hostFailedMessage},
		{hostStatus: http.StatusProxyAuthRequired, wantStatus: http.StatusBadGateway, wantMessage: hostFailedMessage},
		{hostStatus: http.StatusTooManyRequests, wantStatus: http.StatusServiceUnavailable, wantMessage: hostsOverloadedMessage},
	}
	for _, testCase := range testCases {
		t.Run(strconv.Itoa(testCase.hostStatus), func(t *testing.T) {
			live := newHarness(t)
			failRaceWithHostError(live, testCase.hostStatus, hostWrittenMessage)

			recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)

			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d for a host's %d; body %s", recorder.Code, testCase.wantStatus, testCase.hostStatus, recorder.Body)
			}
			if got := decodeErrorMessage(t, recorder.Body.Bytes()); got != testCase.wantMessage {
				t.Fatalf("message = %q, want %q", got, testCase.wantMessage)
			}
		})
	}
}

// Test flow:
//  1. Start a harness whose race streams one content chunk and then fails with a `HostApplicationError` carrying a 401 and a host-written message.
//  2. Send one streaming chat completion.
//  3. Assert the stream answered 200, its error event carries the gateway's message and the host's text appears nowhere in the stream.
func TestAStreamingClientReadsTheGatewaysMessageForAHostError(t *testing.T) {
	live := newHarness(t)
	live.inference.chunks = []string{`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"}
	live.inference.outcome = engine.RaceOutcome{EscrowID: "7"}
	live.inference.err = &engine.HostApplicationError{Code: strconv.Itoa(http.StatusUnauthorized), Type: "invalid_api_key", Message: hostWrittenMessage}

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 once the stream started", recorder.Code)
	}
	want, err := json.Marshal(errorEnvelope{Error: errorDetail{Message: hostFailedMessage}})
	if err != nil {
		t.Fatalf("json.Marshal() = %v", err)
	}
	if !strings.Contains(recorder.Body.String(), "data: "+string(want)) || strings.Contains(recorder.Body.String(), hostWrittenMessage) {
		t.Fatalf("stream = %s, want an error event carrying the gateway's message and not the host's", recorder.Body)
	}
}

// Test flow:
//  1. Start a harness whose race fails with a 400 `HostApplicationError` whose message is a megabyte of multi-byte runes broken by newlines, an invalid byte and a bidirectional override.
//  2. Send one non-streaming chat completion.
//  3. Assert the message is the gateway's prefix and an excerpt of at most `maxHostExcerptBytes` bytes that is valid UTF-8 with no control or format characters.
func TestAHostsRequestFaultMessageReachesTheClientBounded(t *testing.T) {
	const repeatedUnit = "контекст\n\xff\u202e"
	longMessage := strings.Repeat(repeatedUnit, 1<<20/len(repeatedUnit))
	live := newHarness(t)
	failRaceWithHostError(live, http.StatusBadRequest, longMessage)

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %.200s", recorder.Code, recorder.Body)
	}
	message := decodeErrorMessage(t, recorder.Body.Bytes())
	excerpt, prefixed := strings.CutPrefix(message, hostRejectedRequestMessage+hostExcerptSeparator)
	if !prefixed || excerpt == "" || len(excerpt) > maxHostExcerptBytes {
		t.Fatalf("message is %d bytes (%.80q), want the gateway's prefix and a non-empty excerpt of at most %d bytes", len(message), message, maxHostExcerptBytes)
	}
	unprintable := func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError }
	if !utf8.ValidString(excerpt) || strings.ContainsFunc(excerpt, unprintable) {
		t.Fatalf("excerpt = %q, want valid UTF-8 with no control, format or replacement runes", excerpt)
	}
}

// Test flow:
//  1. For each shape and code a host may fold an error into a non-streamed reply after its content, start a harness whose race streams one content chunk and then that error carrying a host-written message.
//  2. Send one non-streaming chat completion.
//  3. Assert the client receives the gateway's status and message for that error, with the host's text only after a request fault's message.
//  4. Assert the body carries neither the content before the error nor the host's text anywhere else.
func TestAHostErrorFoldedIntoAReplyAfterContentIsAnsweredInTheGatewaysWords(t *testing.T) {
	contentEvent := `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"
	testCases := []struct {
		name        string
		errorEvent  string
		wantStatus  int
		wantMessage string
	}{
		{name: "an error object naming a 401", errorEvent: `data: {"error":{"message":"` + hostWrittenMessage + `","code":401}}`, wantStatus: http.StatusBadGateway, wantMessage: hostFailedMessage},
		{name: "an error object naming a 400", errorEvent: `data: {"error":{"message":"` + hostWrittenMessage + `","code":400}}`, wantStatus: http.StatusBadRequest, wantMessage: hostRejectedRequestMessage + hostExcerptSeparator + hostWrittenMessage},
		{name: "an error the host sent as a bare string", errorEvent: `data: {"error":"` + hostWrittenMessage + `"}`, wantStatus: http.StatusBadGateway, wantMessage: hostFailedMessage},
		{name: "the flat shape vLLM answers with", errorEvent: `data: {"object":"error","message":"` + hostWrittenMessage + `","code":503}`, wantStatus: http.StatusServiceUnavailable, wantMessage: hostsOverloadedMessage},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t)
			live.inference.chunks = []string{contentEvent, testCase.errorEvent + "\n\n", "data: [DONE]\n\n"}
			live.inference.outcome = engine.RaceOutcome{EscrowID: "7"}

			recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)

			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", recorder.Code, testCase.wantStatus, recorder.Body)
			}
			if got := decodeErrorMessage(t, recorder.Body.Bytes()); got != testCase.wantMessage {
				t.Fatalf("message = %q, want %q", got, testCase.wantMessage)
			}
			body := recorder.Body.String()
			if strings.Contains(body, "choices") || strings.Count(body, hostWrittenMessage) != strings.Count(testCase.wantMessage, hostWrittenMessage) {
				t.Fatalf("body = %s, want no content and the host's text only inside the gateway's message", body)
			}
		})
	}
}

// Test flow:
//  1. For each code a host may name in an error event it streams after content, terminated, left as the stream's unterminated last event, split across two data lines, sent after the host's own `[DONE]` in the same chunk, sent before it with content after it or sent beside a complete `choices[].message`, start a harness whose race streams that content and event and then fails with the matching `HostApplicationError`, as the engine does for a crowned host's error, or returns no error, as it does for an event whose data lines the classifier read one at a time.
//  2. Send one streaming chat completion.
//  3. Assert the stream answered 200 and kept the content before the error.
//  4. Assert the stream carries exactly one error event, the gateway's, and the host's text only inside a request fault's message, before the stream's only `[DONE]`, and none of a message sent beside the error.
func TestAStreamedHostErrorEventReachesTheClientInTheGatewaysWords(t *testing.T) {
	contentEvent := `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"
	answerBesideTheError := "an answer sent beside the error"
	messageBesideTheError := `"choices":[{"index":0,"message":{"role":"assistant","content":"` + answerBesideTheError + `"},"finish_reason":"stop"}]`
	testCases := []struct {
		name         string
		hostStatus   int
		errorEvent   string
		raceSucceeds bool
		wantMessage  string
	}{
		{name: "a 401", hostStatus: http.StatusUnauthorized, errorEvent: `data: {"error":{"message":"` + hostWrittenMessage + `","code":401}}` + "\n\n", wantMessage: hostFailedMessage},
		{name: "a 400", hostStatus: http.StatusBadRequest, errorEvent: `data: {"error":{"message":"` + hostWrittenMessage + `","code":400}}` + "\n\n", wantMessage: hostRejectedRequestMessage + hostExcerptSeparator + hostWrittenMessage},
		{name: "an unterminated last 401", hostStatus: http.StatusUnauthorized, errorEvent: `data: {"error":{"message":"` + hostWrittenMessage + `","code":401}}`, wantMessage: hostFailedMessage},
		{name: "a 401 split across two data lines", hostStatus: http.StatusUnauthorized, errorEvent: "data: {\"error\":\ndata: {\"message\":\"" + hostWrittenMessage + "\",\"code\":401}}\n\n", raceSucceeds: true, wantMessage: hostFailedMessage},
		{name: "a 401 after the host's [DONE] in the same chunk", hostStatus: http.StatusUnauthorized, errorEvent: "data: [DONE]\n\n" + `data: {"error":{"message":"` + hostWrittenMessage + `","code":401}}` + "\n\n", wantMessage: hostFailedMessage},
		{name: "a 401 after the host's [DONE] in the same chunk of a race that succeeded", hostStatus: http.StatusUnauthorized, errorEvent: "data: [DONE]\n\n" + `data: {"error":{"message":"` + hostWrittenMessage + `","code":401}}` + "\n\n", raceSucceeds: true, wantMessage: hostFailedMessage},
		{name: "a 401 followed by the host's [DONE] and more content in the same chunk", hostStatus: http.StatusUnauthorized, errorEvent: `data: {"error":{"message":"` + hostWrittenMessage + `","code":401}}` + "\n\ndata: [DONE]\n\n" + contentEvent, raceSucceeds: true, wantMessage: hostFailedMessage},
		{name: "a 401 beside a complete message of a race that succeeded", hostStatus: http.StatusUnauthorized, errorEvent: `data: {"error":{"message":"` + hostWrittenMessage + `","code":401},` + messageBesideTheError + "}\n\n", raceSucceeds: true, wantMessage: hostFailedMessage},
		{name: "a 400 beside a complete message", hostStatus: http.StatusBadRequest, errorEvent: `data: {"error":{"message":"` + hostWrittenMessage + `","code":400},` + messageBesideTheError + "}\n\n", wantMessage: hostRejectedRequestMessage + hostExcerptSeparator + hostWrittenMessage},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t)
			live.inference.chunks = []string{contentEvent, testCase.errorEvent}
			live.inference.outcome = engine.RaceOutcome{EscrowID: "7"}
			if !testCase.raceSucceeds {
				live.inference.err = &engine.HostApplicationError{Code: strconv.Itoa(testCase.hostStatus), Message: hostWrittenMessage}
			}

			recorder := live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, nil)

			body := recorder.Body.String()
			if recorder.Code != http.StatusOK || !strings.HasPrefix(body, contentEvent) {
				t.Fatalf("status = %d, stream = %s; want 200 and the content first", recorder.Code, body)
			}
			gatewayEvent, err := json.Marshal(errorEnvelope{Error: errorDetail{Message: testCase.wantMessage}})
			if err != nil {
				t.Fatalf("json.Marshal() = %v", err)
			}
			wantTail := "data: " + string(gatewayEvent) + "\n\ndata: [DONE]\n\n"
			if strings.Count(body, `"error"`) != 1 || strings.Count(body, "[DONE]") != 1 || !strings.HasSuffix(body, wantTail) || strings.Count(body, hostWrittenMessage) != strings.Count(testCase.wantMessage, hostWrittenMessage) || strings.Contains(body, answerBesideTheError) {
				t.Fatalf("stream = %s, want only the gateway's error event %s before [DONE]", body, gatewayEvent)
			}
		})
	}
}

// Test flow:
//  1. Start a harness whose race succeeds after streaming content, a host error event naming a 400 and, in a later write, one naming a 401.
//  2. Send one streaming chat completion.
//  3. Assert the stream ends with the gateway's event for the first error, the 400 with the host's excerpt, and carries no other error event.
func TestTheFirstStreamedHostErrorIsTheOneAnswered(t *testing.T) {
	live := newHarness(t)
	live.inference.chunks = []string{
		`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n",
		`data: {"error":{"message":"` + hostWrittenMessage + `","code":400}}` + "\n\n",
		`data: {"error":{"message":"a later error","code":401}}` + "\n\n",
	}
	live.inference.outcome = engine.RaceOutcome{EscrowID: "7"}

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, nil)

	gatewayEvent, err := json.Marshal(errorEnvelope{Error: errorDetail{Message: hostRejectedRequestMessage + hostExcerptSeparator + hostWrittenMessage}})
	if err != nil {
		t.Fatalf("json.Marshal() = %v", err)
	}
	body := recorder.Body.String()
	if strings.Count(body, `"error"`) != 1 || !strings.HasSuffix(body, "data: "+string(gatewayEvent)+"\n\ndata: [DONE]\n\n") {
		t.Fatalf("stream = %s, want only the gateway's event for the first error %s before [DONE]", body, gatewayEvent)
	}
}
