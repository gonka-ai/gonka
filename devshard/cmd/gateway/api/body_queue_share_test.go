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
//  2. Send two bodies with one key and assert both reach the limiter wait and the budget holds both while they queue.
//  3. Send a third body with the same key and assert it is refused 503 with a Retry-After before it reaches the limiter.
//  4. Send a body with a second key and assert it reaches the limiter wait, so one key's queue does not block another key.
//  5. Admit every queued request and assert all are served 200 and nothing stays reserved.
//  6. Send a body with the first key again and assert it is served.
func TestAQueuedRequestStaysChargedToItsCallerUntilTheLimiterAdmitsIt(t *testing.T) {
	live, body := shareHarness(t, config.ModelAccessAPIKey)
	gated := gateLimiter(t, live, queuedBodyCount)
	queued := queueBodies(t, live, gated, clientKey, body, shareHoldingBody)

	if held, want := live.server.requestBodies.Held(), int64(len(body))*shareHoldingBody; held != want {
		t.Errorf("body bytes reserved while two requests queue = %d, want %d", held, want)
	}
	refused := live.request(t, http.MethodPost, "/v1/chat/completions", body, callerHeaders(clientKey))
	if refused.Code != http.StatusServiceUnavailable || refused.Header().Get("Retry-After") == "" {
		t.Errorf("third body of the queueing key = %d, Retry-After %q (%s), want 503 with a Retry-After", refused.Code, refused.Header().Get("Retry-After"), refused.Body.String())
	}
	select {
	case <-gated.entered:
		t.Error("the refused third body reached the limiter")
	default:
	}
	other := sendQueued(context.Background(), live, otherClientKey, body)
	awaitValue(t, gated.entered, "the second key's request reaching the limiter wait")

	gated.admitAll()
	for _, sender := range append(queued, other) {
		awaitValue(t, sender.handled, "a queued request returning")
		if sender.recorder.Code != http.StatusOK {
			t.Errorf("queued body = %d (%s), want 200 once admitted", sender.recorder.Code, sender.recorder.Body.String())
		}
	}
	assertNothingReserved(t, live, "after every queued request was admitted")
	if again := live.request(t, http.MethodPost, "/v1/chat/completions", body, callerHeaders(clientKey)); again.Code != http.StatusOK {
		t.Errorf("body of the queueing key after admission = %d (%s), want 200", again.Code, again.Body.String())
	}
}

// Test flow:
//  1. Make qwen reachable by API key, give the gateway a body budget of three bodies and a share of two, and queue two bodies of one key at the limiter.
//  2. Give the first a limiter refusal and cancel the second's context.
//  3. Assert the first is answered 429, the second returns, and nothing stays reserved.
//  4. Send two more bodies with the same key and assert both reach the limiter wait, so the charge came back.
func TestARequestTheLimiterRefusesOrAbandonsReturnsItsCharge(t *testing.T) {
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
	queueBodies(t, live, gated, clientKey, body, shareHoldingBody)
}

// Test flow:
//  1. Leave every model at the default admin-only access, give the gateway a body budget of three bodies, and hold the limiter's verdict.
//  2. Send four bodies with the admin key and assert all four reach the limiter wait.
//  3. Assert the budget holds nothing while they queue, since the admin key is not held to a share past normalisation.
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
//  2. Queue 64 ordinary bodies of 64 KiB with one client key and assert all reach the limiter wait.
//  3. Admit them and assert all are served 200 and nothing stays reserved.
func TestOrdinaryQueuedBodiesFitTheDefaultCallerShare(t *testing.T) {
	const ordinaryBodyBytes = 64 << 10
	const ordinaryQueueDepth = 64
	body := chatBodyOfBytes("qwen", ordinaryBodyBytes)
	live := liveWithRealTelemetry(t, func(c *config.Config) {
		c.Server.APIKeys = []string{clientKey}
		grantAccess(c, map[string]string{"qwen": config.ModelAccessAPIKey})
	})
	live.server.requestBodies = NewBufferBudget(config.Defaults().Limits.MaxBufferedRequestBytes)
	gated := gateLimiter(t, live, ordinaryQueueDepth)

	queued := queueBodies(t, live, gated, clientKey, body, ordinaryQueueDepth)

	gated.admitAll()
	for _, sender := range queued {
		awaitValue(t, sender.handled, "a queued request returning")
		if sender.recorder.Code != http.StatusOK {
			t.Errorf("ordinary queued body = %d (%s), want 200", sender.recorder.Code, sender.recorder.Body.String())
		}
	}
	assertNothingReserved(t, live, "after every ordinary queued request was admitted")
}
