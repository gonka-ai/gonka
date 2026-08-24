package signedhttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"trainshard/internal/contract"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/signedhttp"
	"trainshard/internal/utils/timex"
)

type servedStub struct {
	clock *timex.Frozen
	until map[string]time.Time
	err   error
}

func newServedStub() *servedStub {
	return &servedStub{clock: timex.NewFrozen(now), until: map[string]time.Time{}}
}

func (s *servedStub) First(_ context.Context, request string, until time.Time) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	if s.until[request].After(s.clock.Now()) {
		return false, nil
	}
	s.until[request] = until
	return true, nil
}

func served(t *testing.T, address vo.Address, store *servedStub, request signedRequest) (int, string) {
	t.Helper()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	boundary := signedhttp.New(&verifierStub{address: address}, store.clock, window, audience)
	recorder := httptest.NewRecorder()

	boundary.Wrap(signedhttp.NewOnce(store).Wrap(handler)).ServeHTTP(recorder, request.build())

	var envelope contract.Envelope
	if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil || envelope.Error == nil {
		return recorder.Code, ""
	}
	return recorder.Code, envelope.Error.Code
}

func TestASignedRequestIsGoodOnce(t *testing.T) {
	// arrange
	store := newServedStub()
	request := newSignedRequest()
	if code, _ := served(t, "gonka1creator", store, request); code != http.StatusOK {
		t.Fatalf("got %d, want the first request through", code)
	}

	// act
	code, reason := served(t, "gonka1creator", store, request)

	// assert
	if code != http.StatusUnauthorized || reason != "REPLAYED_REQUEST" {
		t.Fatalf("got %d %s, want a caught request refused a second time", code, reason)
	}
}

// A caller whose clock runs ahead is admitted before its timestamp, so its signature outlives any
// span counted from the moment the request arrived
func TestARequestFromAClockAheadIsHeldUntilItsSignatureDies(t *testing.T) {
	// arrange
	store := newServedStub()
	request := newSignedRequest()
	request.timestamp = now.Add(window).Format(time.RFC3339)
	if code, _ := served(t, "gonka1creator", store, request); code != http.StatusOK {
		t.Fatalf("got %d, want a request from a clock one window ahead through", code)
	}
	store.clock.Advance(2*window - time.Nanosecond)

	// act
	code, reason := served(t, "gonka1creator", store, request)

	// assert
	if code != http.StatusUnauthorized || reason != "REPLAYED_REQUEST" {
		t.Fatalf("got %d %s, want the repeat refused while its signature still passes", code, reason)
	}
}

func TestOneCallerCannotSpendAnothersRequestID(t *testing.T) {
	// arrange
	store := newServedStub()
	request := newSignedRequest()
	if code, _ := served(t, "gonka1creator", store, request); code != http.StatusOK {
		t.Fatalf("got %d, want the first request through", code)
	}

	// act
	code, _ := served(t, "gonka1runkey", store, request)

	// assert
	if code != http.StatusOK {
		t.Fatalf("got %d, want each caller counted on its own", code)
	}
}

func TestARequestIsRefusedWhenTheStoreCannotAnswer(t *testing.T) {
	// arrange
	store := newServedStub()
	store.err = context.DeadlineExceeded

	// act
	code, _ := served(t, "gonka1creator", store, newSignedRequest())

	// assert
	if code == http.StatusOK {
		t.Fatal("a request must not be served while the daemon cannot tell whether it already was")
	}
}
