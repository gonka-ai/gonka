package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"devshard/cmd/gateway/config"
)

const (
	otherClientKey   = "other-client-key"
	shareBodyBytes   = 1 << 20
	shareHoldingBody = 2
	shareBudgetBody  = 3
)

type stalledSender struct {
	recorder *httptest.ResponseRecorder
	reached  chan struct{}
	handled  chan struct{}
}

func sendStalledBody(live *harness, key, body string, release <-chan struct{}) *stalledSender {
	sender := &stalledSender{recorder: httptest.NewRecorder(), reached: make(chan struct{}, 1), handled: make(chan struct{})}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", newHeldBody(body, sender.reached, release))
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	go func() {
		defer close(sender.handled)
		live.server.Handler().ServeHTTP(sender.recorder, request)
	}()
	return sender
}

func stallBodies(t *testing.T, live *harness, key, body string, count int) ([]*stalledSender, func()) {
	t.Helper()
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseStalled := func() { releaseOnce.Do(func() { close(release) }) }
	senders := make([]*stalledSender, 0, count)
	t.Cleanup(func() {
		releaseStalled()
		for _, sender := range senders {
			awaitValue(t, sender.handled, "a stalled request returning")
		}
	})
	for range count {
		sender := sendStalledBody(live, key, body, release)
		senders = append(senders, sender)
		awaitValue(t, sender.reached, "a stalled body read to its end")
	}
	return senders, releaseStalled
}

func shareHarness(t *testing.T, access string) (*harness, string) {
	t.Helper()
	body := chatBodyOfBytes("qwen", shareBodyBytes)
	live := liveWithRealTelemetry(t, func(c *config.Config) {
		c.Server.APIKeys = []string{clientKey, otherClientKey}
		grantAccess(c, map[string]string{"qwen": access})
		c.Limits.MaxBufferedRequestBytesPerCaller = int64(len(body)) * shareHoldingBody
	})
	live.server.requestBodies = NewBufferBudget(int64(len(body)) * shareBudgetBody)
	return live, body
}

// Test flow:
//  1. Make qwen reachable by API key only, give the gateway a body budget of three bodies and a per-caller share of two, and let one key send two bodies that deliver every byte and then stall.
//  2. Send a third body with the same key and assert it is refused 503 with a Retry-After while the first two hold their bytes and the budget has a free slot.
//  3. Send a body with a second key and assert it is served 200 although the first key is stalling.
//  4. Release the stalled bodies and assert both are served, nothing stays reserved and no caller keeps an entry.
//  5. Send a body with the first key again and assert it is served.
func TestACallerThatStallsItsBodiesCannotHoldMoreThanItsShareOfTheBodyBudget(t *testing.T) {
	live, body := shareHarness(t, config.ModelAccessAPIKey)
	stalled, releaseStalled := stallBodies(t, live, clientKey, body, shareHoldingBody)

	refused := live.request(t, http.MethodPost, "/v1/chat/completions", body, callerHeaders(clientKey))
	admitted := live.request(t, http.MethodPost, "/v1/chat/completions", body, callerHeaders(otherClientKey))

	if refused.Code != http.StatusServiceUnavailable || refused.Header().Get("Retry-After") == "" {
		t.Errorf("third body of the stalling key = %d, Retry-After %q (%s), want 503 with a Retry-After", refused.Code, refused.Header().Get("Retry-After"), refused.Body.String())
	}
	if admitted.Code != http.StatusOK {
		t.Errorf("body of a second key = %d (%s), want 200 while the first key stalls", admitted.Code, admitted.Body.String())
	}
	if held, want := live.server.requestBodies.Held(), int64(len(body))*shareHoldingBody; held != want {
		t.Errorf("body bytes reserved while two bodies stall = %d, want %d", held, want)
	}

	releaseStalled()
	for _, sender := range stalled {
		awaitValue(t, sender.handled, "a stalled request returning")
		if sender.recorder.Code != http.StatusOK {
			t.Errorf("stalled body = %d (%s), want 200 once released", sender.recorder.Code, sender.recorder.Body.String())
		}
	}
	if held, callers := live.server.requestBodies.Held(), len(live.server.requestBodies.callers); held != 0 || callers != 0 {
		t.Errorf("after release: %d bytes reserved and %d callers tracked, want 0 and 0", held, callers)
	}
	if again := live.request(t, http.MethodPost, "/v1/chat/completions", body, callerHeaders(clientKey)); again.Code != http.StatusOK {
		t.Errorf("body of the first key after release = %d (%s), want 200", again.Code, again.Body.String())
	}
}

// Test flow:
//  1. Give the gateway a body budget of the default size.
//  2. Send an ordinary chat body and assert it is served 200.
//  3. Assert nothing stays reserved and no caller keeps an entry.
func TestAnOrdinaryBodyIsServedWithinTheDefaultCallerShare(t *testing.T) {
	live := newHarness(t)
	live.server.requestBodies = NewBufferBudget(config.Defaults().Limits.MaxBufferedRequestBytes)

	served := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders(clientKey))

	if served.Code != http.StatusOK {
		t.Errorf("ordinary body = %d (%s), want 200", served.Code, served.Body.String())
	}
	if held, callers := live.server.requestBodies.Held(), len(live.server.requestBodies.callers); held != 0 || callers != 0 {
		t.Errorf("after serving: %d bytes reserved and %d callers tracked, want 0 and 0", held, callers)
	}
}

// Test flow:
//  1. Set the body budget to 16 MiB and leave the per-caller share at its default of 32 MiB.
//  2. Assert an api key caller and a keyless caller get a share of 16 MiB, the budget, not 32 MiB.
//  3. Assert the admin key gets the whole 16 MiB budget.
//  4. Raise the budget to the default and assert a non-admin caller gets the 32 MiB share while the admin gets the whole budget.
func TestABodyBudgetBelowTheCallerShareCapsANonAdminCallerAtTheBudget(t *testing.T) {
	configuration := config.Defaults()
	configuration.Limits.MaxBufferedRequestBytes = 16 << 20
	if configuration.Limits.MaxBufferedRequestBytes >= configuration.Limits.MaxBufferedRequestBytesPerCaller {
		t.Fatalf("budget %d is not below the default share %d, so the test proves nothing", configuration.Limits.MaxBufferedRequestBytes, configuration.Limits.MaxBufferedRequestBytesPerCaller)
	}

	for name, identity := range map[string]credentials{"api key": {apiKey: true, apiKeyID: "k"}, "keyless": {}} {
		if got := bodyShareLimit(&configuration, identity); got != 16<<20 {
			t.Errorf("share of a %s caller under a 16 MiB budget = %d, want %d", name, got, 16<<20)
		}
	}
	if got := bodyShareLimit(&configuration, credentials{admin: true}); got != 16<<20 {
		t.Errorf("share of the admin key under a 16 MiB budget = %d, want %d", got, 16<<20)
	}

	configuration.Limits.MaxBufferedRequestBytes = config.Defaults().Limits.MaxBufferedRequestBytes
	if got := bodyShareLimit(&configuration, credentials{apiKey: true, apiKeyID: "k"}); got != 32<<20 {
		t.Errorf("share of an api key caller under the default budget = %d, want %d", got, 32<<20)
	}
	if got := bodyShareLimit(&configuration, credentials{admin: true}); got != configuration.Limits.MaxBufferedRequestBytes {
		t.Errorf("share of the admin key under the default budget = %d, want %d", got, configuration.Limits.MaxBufferedRequestBytes)
	}
}

// Test flow:
//  1. Give one caller a share of 100 bytes in a budget of 1000 and read a 60 byte body for it.
//  2. Read a 60 byte body for a second caller and assert it is read in full.
//  3. Read a second 60 byte body for the first caller and assert it is refused with `ErrRequestBodiesFull`.
//  4. Assert the refusal gave back what that read had charged: the first body's 60 bytes and the second caller's 60 are all that stay reserved.
//  5. Give both bodies back and assert nothing is reserved and no caller is tracked.
func TestARefusedCallerGivesBackOnlyWhatItsOwnReadCharged(t *testing.T) {
	budget := NewBufferBudget(1000)
	first := budget.forCaller("first", 100)
	second := budget.forCaller("second", 100)
	sixty := strings.Repeat("a", 60)

	firstBody, err := readAll(strings.NewReader(sixty), first)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	secondBody, err := readAll(strings.NewReader(sixty), second)
	if err != nil {
		t.Fatalf("second caller's read: %v", err)
	}
	_, err = readAll(strings.NewReader(sixty), first)

	if !errors.Is(err, ErrRequestBodiesFull) {
		t.Errorf("read past the share = %v, want ErrRequestBodiesFull", err)
	}
	if held := budget.Held(); held != 120 {
		t.Errorf("reserved after the refusal = %d, want 120", held)
	}
	first.release(int64(len(firstBody)))
	second.release(int64(len(secondBody)))
	if held, callers := budget.Held(), len(budget.callers); held != 0 || callers != 0 {
		t.Errorf("after giving back: %d bytes reserved and %d callers tracked, want 0 and 0", held, callers)
	}
}

// Test flow:
//  1. Leave qwen admin-only, give the gateway a body budget of three bodies and a per-caller share of two, and send three bodies with the admin key that deliver every byte and stall.
//  2. Assert all three were read and the budget holds all three, since the admin key takes no share.
//  3. Release them and assert each is served 200 and nothing stays reserved.
func TestTheAdminKeyIsNotHeldToTheCallerShareOfTheBodyBudget(t *testing.T) {
	live, body := shareHarness(t, config.ModelAccessAdminOnly)

	stalled, releaseStalled := stallBodies(t, live, adminKey, body, shareBudgetBody)

	if held, want := live.server.requestBodies.Held(), int64(len(body))*shareBudgetBody; held != want {
		t.Errorf("body bytes reserved while three admin bodies stall = %d, want %d", held, want)
	}
	releaseStalled()
	for _, sender := range stalled {
		awaitValue(t, sender.handled, "a stalled admin request returning")
		if sender.recorder.Code != http.StatusOK {
			t.Errorf("admin body = %d (%s), want 200", sender.recorder.Code, sender.recorder.Body.String())
		}
	}
	assertNothingReserved(t, live, "after release")
}

// Test flow:
//  1. Open qwen to every caller, give the gateway a body budget of three bodies and a share of two, and stall two bodies from one credential: none at all, then a key no one configured.
//  2. Send a body with no key and a body with an unknown key and assert both are refused 503, so neither lands in an exempt bucket.
//  3. Send a body with a client key and a body with the admin key and assert both are served 200 while the other credentials stall.
func TestKeylessAndUnknownKeyCallersShareOneCappedBucket(t *testing.T) {
	const unknownKey = "no-such-key"
	for name, stalling := range map[string]string{"keyless": "", "unknown key": unknownKey} {
		t.Run(name, func(t *testing.T) {
			live, body := shareHarness(t, config.ModelAccessOpen)
			stallBodies(t, live, stalling, body, shareHoldingBody)

			keyless := live.request(t, http.MethodPost, "/v1/chat/completions", body, nil)
			unknown := live.request(t, http.MethodPost, "/v1/chat/completions", body, callerHeaders(unknownKey))
			client := live.request(t, http.MethodPost, "/v1/chat/completions", body, callerHeaders(clientKey))
			admin := live.request(t, http.MethodPost, "/v1/chat/completions", body, adminHeaders())

			if keyless.Code != http.StatusServiceUnavailable || unknown.Code != http.StatusServiceUnavailable {
				t.Errorf("body with no key = %d, with an unknown key = %d, want 503 for both", keyless.Code, unknown.Code)
			}
			if client.Code != http.StatusOK || admin.Code != http.StatusOK {
				t.Errorf("body with a client key = %d, with the admin key = %d, want 200 for both", client.Code, admin.Code)
			}
		})
	}
}
