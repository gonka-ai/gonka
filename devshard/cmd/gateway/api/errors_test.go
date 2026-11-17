package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/scheduler"
	"devshard/types"
)

func newErrorsTestServer() *Server {
	defaults := config.Defaults()
	return &Server{config: config.NewHolder(&defaults)}
}

// Test flow:
//  1. Write a `RateLimitError` with a 1.5s retry-after through `writeErrorFor`.
//  2. Assert the response status is 429.
//  3. Assert the Retry-After header is "2" (1.5s rounded up).
func TestARateLimitRejectionCarriesRetryAfter(t *testing.T) {
	recorder := httptest.NewRecorder()

	newErrorsTestServer().writeErrorFor(recorder, &limits.RateLimitError{Reason: "queue timeout", RetryAfter: 1500 * time.Millisecond})

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
	if got := recorder.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want %q (1.5s rounded up)", got, "2")
	}
}

// Test flow:
//  1. Write `ErrPrivateKeyEnvRequired` through `writeErrorFor`.
//  2. Assert the response carries no Retry-After header.
func TestOtherRejectionsCarryNoRetryAfter(t *testing.T) {
	recorder := httptest.NewRecorder()

	newErrorsTestServer().writeErrorFor(recorder, ErrPrivateKeyEnvRequired)

	if got := recorder.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After = %q on a client-fixable rejection, want none", got)
	}
}

// Test flow:
//  1. Write `scheduler.ErrHostsBusy` through `writeErrorFor`.
//  2. Assert the response status is 503, not 502.
//  3. Assert the Retry-After header is "1".
func TestAHostlessRequestIsOurRefusalNotAnUpstreamFailure(t *testing.T) {
	recorder := httptest.NewRecorder()

	newErrorsTestServer().writeErrorFor(recorder, scheduler.ErrHostsBusy)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if got := recorder.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want a wait the client can act on", got)
	}
}

// Test flow:
//  1. For each capacity-refusal error in `ErrHostsBusy`, `ErrNoEscrowCapacity` and `ErrEscrowBusy`, write it through `writeErrorFor`.
//  2. Assert every one answers 503, not 429.
//  3. Assert every one carries a non-empty Retry-After header.
func TestACapacityRefusalAnswersUnavailableWithAWait(t *testing.T) {
	refusals := []error{scheduler.ErrHostsBusy, scheduler.ErrNoEscrowCapacity, scheduler.ErrEscrowBusy}
	for _, refusal := range refusals {
		recorder := httptest.NewRecorder()
		newErrorsTestServer().writeErrorFor(recorder, refusal)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%v answered %d, want 503", refusal, recorder.Code)
		}
		if recorder.Header().Get("Retry-After") == "" {
			t.Fatalf("%v carried no Retry-After: a client cannot tell when to come back", refusal)
		}
	}
}

// Test flow:
//  1. Write a `ModelUnavailableError` for model "qwen" through `writeErrorFor`.
//  2. Assert the response status is 503.
//  3. Assert Retry-After equals the escrow tick interval, in seconds.
func TestAModelUnavailableRejectionCarriesTheEscrowTickInterval(t *testing.T) {
	recorder := httptest.NewRecorder()

	newErrorsTestServer().writeErrorFor(recorder, &ModelUnavailableError{Model: "qwen"})

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	want := strconv.Itoa(int(escrow.TickInterval.Seconds()))
	if got := recorder.Header().Get("Retry-After"); got != want {
		t.Fatalf("Retry-After = %q, want %q (the escrow tick interval)", got, want)
	}
}

// Test flow:
//  1. Write an error wrapping both `scheduler.ErrNoEscrowCapacity` and `types.ErrInsufficientBalance` through `writeErrorFor`.
//  2. Assert the response status is 503.
//  3. Assert Retry-After equals the escrow tick interval, in seconds.
func TestAFundingRefusalCarriesTheEscrowTickInterval(t *testing.T) {
	recorder := httptest.NewRecorder()

	newErrorsTestServer().writeErrorFor(recorder, fmt.Errorf("%w: %w", scheduler.ErrNoEscrowCapacity, types.ErrInsufficientBalance))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	want := strconv.Itoa(int(escrow.TickInterval.Seconds()))
	if got := recorder.Header().Get("Retry-After"); got != want {
		t.Fatalf("Retry-After = %q, want %q (the escrow tick interval)", got, want)
	}
}

// Test flow:
//  1. Write `scheduler.ErrNoEscrowCapacity` alone through `writeErrorFor`.
//  2. Assert Retry-After equals `noHostRetryAfter`, in seconds, the short retry rather than the escrow tick interval.
func TestABusyShardKeepsTheShortRetry(t *testing.T) {
	recorder := httptest.NewRecorder()

	newErrorsTestServer().writeErrorFor(recorder, scheduler.ErrNoEscrowCapacity)

	want := strconv.Itoa(int(noHostRetryAfter.Seconds()))
	if got := recorder.Header().Get("Retry-After"); got != want {
		t.Fatalf("Retry-After = %q, want %q", got, want)
	}
}

// Test flow:
//  1. Write a `RateLimitError` for "too many concurrent requests" through `writeErrorFor`.
//  2. Assert the response status is 429.
//  3. Assert the response carries a non-empty Retry-After header.
func TestTheGatewaysOwnLimitAnswersTooManyRequests(t *testing.T) {
	recorder := httptest.NewRecorder()

	newErrorsTestServer().writeErrorFor(recorder, &limits.RateLimitError{Reason: "too many concurrent requests"})

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
	if recorder.Header().Get("Retry-After") == "" {
		t.Fatal("a limiter rejection carried no Retry-After")
	}
}

// Test flow:
//  1. Write `engine.ErrPoCTookTheHosts` through `writeErrorFor` on a default-configured server.
//  2. Assert the response is a retryable 503 with a Retry-After, not a 502: proof of compute took the hosts, nothing about the request failed.
func TestARaceProofOfComputeEndedIsARetryableUnavailable(t *testing.T) {
	recorder := httptest.NewRecorder()

	newErrorsTestServer().writeErrorFor(recorder, engine.ErrPoCTookTheHosts)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	if recorder.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After is missing: a client has no hint when proof of compute lets the hosts go")
	}
}

// Test flow:
//  1. Write `engine.ErrHostsUnavailable` through `writeErrorFor` on a default-configured server.
//  2. Assert the response status is 503.
//  3. Assert Retry-After is "5", the default host cutoff base rounded up to seconds.
func TestAHostsUnavailableRejectionCarriesTheHostCutoffBase(t *testing.T) {
	recorder := httptest.NewRecorder()

	newErrorsTestServer().writeErrorFor(recorder, engine.ErrHostsUnavailable)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	if got := recorder.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want %q (the default host cutoff base, rounded up to seconds)", got, "5")
	}
}

// Test flow:
//  1. Build a server configured with a 2500ms host cutoff base.
//  2. Write `engine.ErrHostsUnavailable` through `writeErrorFor`.
//  3. Assert Retry-After is "3" (2.5s rounded up).
func TestAHostsUnavailableRejectionRoundsUpAConfiguredBase(t *testing.T) {
	recorder := httptest.NewRecorder()
	configuration := config.Defaults()
	configuration.Limits.HostCutoff.BaseMS = 2_500
	server := &Server{config: config.NewHolder(&configuration)}

	server.writeErrorFor(recorder, engine.ErrHostsUnavailable)

	if got := recorder.Header().Get("Retry-After"); got != "3" {
		t.Fatalf("Retry-After = %q, want %q (2.5s rounded up)", got, "3")
	}
}
