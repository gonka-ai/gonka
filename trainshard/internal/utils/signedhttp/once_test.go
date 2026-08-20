package signedhttp_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"trainshard/internal/contract"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/signedhttp"
	"trainshard/internal/utils/timex"
)

func served(t *testing.T, address vo.Address, once *signedhttp.Once, request signedRequest) (int, string) {
	t.Helper()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	boundary := signedhttp.New(&verifierStub{address: address}, timex.NewFrozen(now), window)
	recorder := httptest.NewRecorder()

	boundary.Wrap(once.Wrap(handler)).ServeHTTP(recorder, request.build())

	var envelope contract.Envelope
	if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil || envelope.Error == nil {
		return recorder.Code, ""
	}
	return recorder.Code, envelope.Error.Code
}

func TestASignedRequestIsGoodOnce(t *testing.T) {

	clock := timex.NewFrozen(now)
	once := signedhttp.NewOnce(clock, window)
	request := newSignedRequest()
	if code, _ := served(t, "gonka1creator", once, request); code != http.StatusOK {
		t.Fatalf("got %d, want the first request through", code)
	}

	code, reason := served(t, "gonka1creator", once, request)

	if code != http.StatusUnauthorized || reason != "REPLAYED_REQUEST" {
		t.Fatalf("got %d %s, want a caught request refused a second time", code, reason)
	}
	clock.Advance(2 * window)
	if code, _ := served(t, "gonka1creator", once, request); code != http.StatusOK {
		t.Fatalf("got %d, want ids forgotten once their signature is stale anyway", code)
	}
}

func TestOneCallerCannotSpendAnothersRequestID(t *testing.T) {

	once := signedhttp.NewOnce(timex.NewFrozen(now), window)
	request := newSignedRequest()
	if code, _ := served(t, "gonka1creator", once, request); code != http.StatusOK {
		t.Fatalf("got %d, want the first request through", code)
	}

	code, _ := served(t, "gonka1runkey", once, request)

	if code != http.StatusOK {
		t.Fatalf("got %d, want each caller counted on its own", code)
	}
}
