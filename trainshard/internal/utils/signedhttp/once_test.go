package signedhttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"trainshard/internal/contract"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/signedhttp"
	"trainshard/internal/utils/timex"
)

type servedStub struct {
	spent map[string]bool
	err   error
}

func newServedStub() *servedStub { return &servedStub{spent: map[string]bool{}} }

func (s *servedStub) First(_ context.Context, request string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	if s.spent[request] {
		return false, nil
	}
	s.spent[request] = true
	return true, nil
}

func served(t *testing.T, address vo.Address, once *signedhttp.Once, request signedRequest) (int, string) {
	t.Helper()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	boundary := signedhttp.New(&verifierStub{address: address}, timex.NewFrozen(now), window, audience)
	recorder := httptest.NewRecorder()

	boundary.Wrap(once.Wrap(handler)).ServeHTTP(recorder, request.build())

	var envelope contract.Envelope
	if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil || envelope.Error == nil {
		return recorder.Code, ""
	}
	return recorder.Code, envelope.Error.Code
}

func TestASignedRequestIsGoodOnce(t *testing.T) {
	// arrange
	once := signedhttp.NewOnce(newServedStub())
	request := newSignedRequest()
	if code, _ := served(t, "gonka1creator", once, request); code != http.StatusOK {
		t.Fatalf("got %d, want the first request through", code)
	}

	// act
	code, reason := served(t, "gonka1creator", once, request)

	// assert
	if code != http.StatusUnauthorized || reason != "REPLAYED_REQUEST" {
		t.Fatalf("got %d %s, want a caught request refused a second time", code, reason)
	}
}

func TestOneCallerCannotSpendAnothersRequestID(t *testing.T) {
	// arrange
	once := signedhttp.NewOnce(newServedStub())
	request := newSignedRequest()
	if code, _ := served(t, "gonka1creator", once, request); code != http.StatusOK {
		t.Fatalf("got %d, want the first request through", code)
	}

	// act
	code, _ := served(t, "gonka1runkey", once, request)

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
	code, _ := served(t, "gonka1creator", signedhttp.NewOnce(store), newSignedRequest())

	// assert
	if code == http.StatusOK {
		t.Fatal("a request must not be served while the daemon cannot tell whether it already was")
	}
}
