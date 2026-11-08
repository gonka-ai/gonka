package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/limits"
)

const (
	firstCallerKey  = "first-caller-api-key"
	secondCallerKey = "second-caller-api-key"
)

func twoCallerKeys(configuration *config.Config) {
	configuration.Server.APIKeys = []string{firstCallerKey, secondCallerKey}
	configuration.Limits.ModelAccess = map[string]string{"qwen": config.ModelAccessAPIKey}
}

func distinctChatBody(index int) string {
	return fmt.Sprintf(`{"model":"qwen","messages":[{"role":"user","content":"request %d"}]}`, index)
}

func apiKeyIDFor(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])[:8]
}

func linesWithMessage(logged *logcapture.Recorder, message string) []logcapture.Entry {
	var lines []logcapture.Entry
	for _, entry := range logged.All() {
		if entry.Msg == message {
			lines = append(lines, entry)
		}
	}
	return lines
}

func withoutField(entry logcapture.Entry, key string) logcapture.Entry {
	kept := logcapture.Entry{Level: entry.Level, Msg: entry.Msg}
	for index := 0; index+1 < len(entry.Fields); index += 2 {
		if entry.Fields[index] != key {
			kept.Fields = append(kept.Fields, entry.Fields[index], entry.Fields[index+1])
		}
	}
	return kept
}

// Test flow:
//  1. Start a harness with two client API keys and the model on the `api_key` tier, logging through a capture.
//  2. Send the same chat completion once with each key and flush the journal after each.
//  3. Assert each "request finished" line names its key by the first eight hex characters of the key's SHA-256, never by the key itself.
//  4. Assert the two lines are otherwise identical field for field.
func TestEachRequestRecordNamesItsAPIKey(t *testing.T) {
	logged := logcapture.Install(t)
	live := newHarness(t, twoCallerKeys)

	keys := []string{firstCallerKey, secondCallerKey}
	for _, key := range keys {
		recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders(key))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status with a configured key = %d, want 200; body %s", recorder.Code, recorder.Body)
		}
		live.events.Flush()
	}

	finished := linesWithMessage(logged, "request finished")
	if len(finished) != len(keys) {
		t.Fatalf("request finished lines = %d, want %d", len(finished), len(keys))
	}
	for index, key := range keys {
		if got := logcapture.Field(finished[index], "api_key_id"); got != apiKeyIDFor(key) {
			t.Fatalf("api_key_id on the %s request = %v, want %s", key, got, apiKeyIDFor(key))
		}
		if strings.Contains(fmt.Sprint(finished[index].Fields), key) {
			t.Fatalf("request finished line %+v carries the key itself", finished[index])
		}
	}
	if !reflect.DeepEqual(withoutField(finished[0], "api_key_id"), withoutField(finished[1], "api_key_id")) {
		t.Fatalf("the two keys' records differ beyond the key id:\n%+v\n%+v", finished[0], finished[1])
	}
}

// Test flow:
//  1. Start a harness with two client API keys and the gateway's real limiter capped at one concurrent request for the model, with no admission queue wait.
//  2. Send a chat completion with the first key and keep its race's finish hook, so it holds the only slot.
//  3. Send a chat completion with the second key and flush the journal.
//  4. Assert the refusal is a 429 and its "gateway limiter turned a request away" line names the second key's id.
func TestALimiterRefusalNamesTheAPIKey(t *testing.T) {
	logged := logcapture.Install(t)
	live := newHarness(t, twoCallerKeys)
	live.server.limiter = limits.NewGatewayLimiter(limits.GatewayConfig{MaxConcurrent: 1})
	live.inference.finishes = make(chan func(), 1)

	if recorder := live.request(t, http.MethodPost, "/v1/chat/completions", distinctChatBody(0), callerHeaders(firstCallerKey)); recorder.Code != http.StatusOK {
		t.Fatalf("first caller status = %d, want 200; body %s", recorder.Code, recorder.Body)
	}
	refused := live.request(t, http.MethodPost, "/v1/chat/completions", distinctChatBody(1), callerHeaders(secondCallerKey))
	live.events.Flush()
	(<-live.inference.finishes)()

	if refused.Code != http.StatusTooManyRequests {
		t.Fatalf("second caller status = %d, want 429; body %s", refused.Code, refused.Body)
	}
	throttled := linesWithMessage(logged, "gateway limiter turned a request away")
	if len(throttled) != 1 || logcapture.Field(throttled[0], "api_key_id") != apiKeyIDFor(secondCallerKey) {
		t.Fatalf("limiter lines = %+v, want one naming %s", throttled, apiKeyIDFor(secondCallerKey))
	}
}

// Test flow:
//  1. Start a harness with two client API keys and the gateway's real limiter capped at two concurrent requests for the model, with no admission queue wait.
//  2. Send two different chat completions with the first key, so the response cache answers neither, and keep both races' finish hooks, so both hold their limiter slots.
//  3. Send one chat completion with the second key and assert it is refused with 429 by the gateway's own limiter.
//  4. Finish one of the first key's races, send the second key's request again and assert it is served.
func TestOneAPIKeyCanHoldEveryModelSlotAndStarveAnother(t *testing.T) {
	const modelConcurrency = 2
	live := newHarness(t, twoCallerKeys)
	live.server.limiter = limits.NewGatewayLimiter(limits.GatewayConfig{MaxConcurrent: modelConcurrency})
	live.inference.finishes = make(chan func(), modelConcurrency)

	for index := range modelConcurrency {
		if recorder := live.request(t, http.MethodPost, "/v1/chat/completions", distinctChatBody(index), callerHeaders(firstCallerKey)); recorder.Code != http.StatusOK {
			t.Fatalf("first caller status = %d, want 200; body %s", recorder.Code, recorder.Body)
		}
	}

	refused := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders(secondCallerKey))
	if refused.Code != http.StatusTooManyRequests {
		t.Fatalf("second caller status = %d, want 429 while the first caller holds every slot; body %s", refused.Code, refused.Body)
	}

	(<-live.inference.finishes)()
	served := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders(secondCallerKey))
	if served.Code != http.StatusOK {
		t.Fatalf("second caller status after a slot freed = %d, want 200; body %s", served.Code, served.Body)
	}
	for range len(live.inference.finishes) {
		(<-live.inference.finishes)()
	}
}
