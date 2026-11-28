package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/limits"
)

const queuedBodyCount = 4

type gatedLimiter struct {
	*countingLimiter
	entered  chan struct{}
	refusals chan error
	admit    chan struct{}
	admitAll func()
}

func (l gatedLimiter) AcquireForModel(ctx context.Context, model string, inputTokens int64, capacity limits.ModelCapacity) error {
	l.entered <- struct{}{}
	select {
	case err := <-l.refusals:
		return err
	case <-l.admit:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(exchangeBound):
		return errors.New("the test never gave the limiter a verdict")
	}
	return l.countingLimiter.AcquireForModel(ctx, model, inputTokens, capacity)
}

type queuedSender struct {
	recorder *httptest.ResponseRecorder
	handled  chan struct{}
}

func gateLimiter(t *testing.T, live *harness, depth int) gatedLimiter {
	t.Helper()
	admit := make(chan struct{})
	gated := gatedLimiter{countingLimiter: live.limiter, entered: make(chan struct{}, depth), refusals: make(chan error, depth), admit: admit, admitAll: sync.OnceFunc(func() { close(admit) })}
	live.server.limiter = gated
	t.Cleanup(gated.admitAll)
	return gated
}

func sendQueued(ctx context.Context, live *harness, key, body string) *queuedSender {
	sender := &queuedSender{recorder: httptest.NewRecorder(), handled: make(chan struct{})}
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	go func() {
		defer close(sender.handled)
		live.server.Handler().ServeHTTP(sender.recorder, request)
	}()
	return sender
}

func queueBodies(t *testing.T, live *harness, gated gatedLimiter, key, body string, count int) []*queuedSender {
	t.Helper()
	senders := make([]*queuedSender, 0, count)
	for range count {
		senders = append(senders, sendQueued(context.Background(), live, key, body))
		awaitValue(t, gated.entered, "a request reaching the limiter wait")
	}
	t.Cleanup(func() {
		gated.admitAll()
		for _, sender := range senders {
			awaitValue(t, sender.handled, "a queued request returning")
		}
	})
	return senders
}

func assertNothingReserved(t *testing.T, live *harness, when string) {
	t.Helper()
	if held, callers := live.server.requestBodies.Held(), len(live.server.requestBodies.callers); held != 0 || callers != 0 {
		t.Errorf("%s: %d bytes reserved and %d callers tracked, want 0 and 0", when, held, callers)
	}
}

// Test flow:
//  1. Make qwen reachable by API key, give the gateway a body budget of three bodies and a share of two, and hold the limiter's verdict.
//  2. Queue four bodies with one key, more than its share and more than the whole budget would hold, and assert every one reaches the limiter wait.
//  3. Assert the budget reserves nothing and tracks no caller while they queue.
//  4. Send a body with a second key and assert it reaches the limiter wait too.
//  5. Admit every queued request and assert all are served 200 and nothing stays reserved.
func TestARequestQueuedAtTheLimiterHoldsNoBodyBudget(t *testing.T) {
	live, body := shareHarness(t, config.ModelAccessAPIKey)
	gated := gateLimiter(t, live, queuedBodyCount+1)
	queued := queueBodies(t, live, gated, clientKey, body, queuedBodyCount)

	assertNothingReserved(t, live, "while four bodies of one key queue")
	other := sendQueued(context.Background(), live, otherClientKey, body)
	awaitValue(t, gated.entered, "the second key's request reaching the limiter wait")
	assertNothingReserved(t, live, "while five bodies of two keys queue")

	gated.admitAll()
	for _, sender := range append(queued, other) {
		awaitValue(t, sender.handled, "a queued request returning")
		if sender.recorder.Code != http.StatusOK {
			t.Errorf("queued body = %d (%s), want 200 once admitted", sender.recorder.Code, sender.recorder.Body.String())
		}
	}
	assertNothingReserved(t, live, "after every queued request was admitted")
}

// Test flow:
//  1. Make qwen reachable by API key, give the gateway a body budget of three bodies and a share of two, and queue two bodies of one key at the limiter.
//  2. Give the first a limiter refusal and cancel the second's context.
//  3. Assert the first is answered 429, the second returns, and nothing stays reserved.
//  4. Send four more bodies with the same key and assert all reach the limiter wait.
func TestARequestTheLimiterRefusesOrAbandonsLeavesNothingReserved(t *testing.T) {
	live, body := shareHarness(t, config.ModelAccessAPIKey)
	gated := gateLimiter(t, live, queuedBodyCount)
	refusedSender := sendQueued(context.Background(), live, clientKey, body)
	awaitValue(t, gated.entered, "the first request reaching the limiter wait")
	cancelled, cancel := context.WithCancel(context.Background())
	abandonedSender := sendQueued(cancelled, live, clientKey, body)
	awaitValue(t, gated.entered, "the second request reaching the limiter wait")

	cancel()
	awaitValue(t, abandonedSender.handled, "the abandoned request returning")
	gated.refusals <- &limits.RateLimitError{Reason: "queue is deeper than the wait budget can clear", RetryAfter: time.Second}
	awaitValue(t, refusedSender.handled, "the refused request returning")

	if refusedSender.recorder.Code != http.StatusTooManyRequests {
		t.Errorf("request the limiter refused = %d (%s), want 429", refusedSender.recorder.Code, refusedSender.recorder.Body.String())
	}
	assertNothingReserved(t, live, "after one request was refused and one abandoned")
	queueBodies(t, live, gated, clientKey, body, queuedBodyCount)
}

// Test flow:
//  1. Leave every model at the default admin-only access, give the gateway a body budget of three bodies, and hold the limiter's verdict.
//  2. Send four bodies with the admin key and assert all four reach the limiter wait.
//  3. Assert the budget holds nothing while they queue.
//  4. Admit them and assert all are served 200.
func TestAnAdminRequestQueuedAtTheLimiterHoldsNoBodyBudget(t *testing.T) {
	body := chatBodyOfBytes("qwen", shareBodyBytes)
	live := liveWithRealTelemetry(t, func(c *config.Config) { grantAccess(c, map[string]string{}) })
	live.server.requestBodies = NewBufferBudget(int64(len(body)) * shareBudgetBody)
	gated := gateLimiter(t, live, queuedBodyCount)

	queued := queueBodies(t, live, gated, adminKey, body, queuedBodyCount)

	assertNothingReserved(t, live, "while four admin requests queue")
	gated.admitAll()
	for _, sender := range queued {
		awaitValue(t, sender.handled, "a queued admin request returning")
		if sender.recorder.Code != http.StatusOK {
			t.Errorf("queued admin body = %d (%s), want 200", sender.recorder.Code, sender.recorder.Body.String())
		}
	}
}

// Test flow:
//  1. Open qwen to every caller with the default body budget and share, and hold the limiter's verdict.
//  2. Queue 24 bodies of 1.6 MB, about 400k tokens, with one client key, together more than the default caller share, and assert all reach the limiter wait.
//  3. Assert the budget reserves nothing while they queue.
//  4. Admit them and assert all are served 200 and nothing stays reserved.
func TestQueuedBodiesBeyondTheDefaultCallerShareAreAllAdmittedToTheQueue(t *testing.T) {
	const largeBodyBytes = 1_600_000
	const largeQueueDepth = 24
	body := chatBodyOfBytes("qwen", largeBodyBytes)
	live := liveWithRealTelemetry(t, func(c *config.Config) {
		c.Server.APIKeys = []string{clientKey}
		grantAccess(c, map[string]string{"qwen": config.ModelAccessAPIKey})
	})
	defaults := config.Defaults().Limits
	live.server.requestBodies = NewBufferBudget(defaults.MaxBufferedRequestBytes)
	if queuedBytes := int64(len(body)) * largeQueueDepth; queuedBytes <= defaults.MaxBufferedRequestBytesPerCaller {
		t.Fatalf("%d queued bytes do not exceed the default caller share of %d", queuedBytes, defaults.MaxBufferedRequestBytesPerCaller)
	}
	gated := gateLimiter(t, live, largeQueueDepth)

	queued := queueBodies(t, live, gated, clientKey, body, largeQueueDepth)

	assertNothingReserved(t, live, "while the large bodies queue")
	gated.admitAll()
	for _, sender := range queued {
		awaitValue(t, sender.handled, "a queued request returning")
		if sender.recorder.Code != http.StatusOK {
			t.Errorf("large queued body = %d (%s), want 200", sender.recorder.Code, sender.recorder.Body.String())
		}
	}
	assertNothingReserved(t, live, "after every large queued request was admitted")
}
