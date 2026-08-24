package signedhttp_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"trainshard/internal/contract"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/signedhttp"
	"trainshard/internal/utils/timex"
)

const (
	window   = time.Minute
	path     = "/trainshard/v0/shards/7/deploy"
	audience = vo.Address("gonka1host")
)

var now = time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

type verifierStub struct {
	address vo.Address
	payload []byte
	err     error
}

func (v *verifierStub) Recover(payload, _ []byte) (vo.Address, error) {
	v.payload = payload
	return v.address, v.err
}

type boundVerifier struct {
	signed  []byte
	address vo.Address
}

func (v *boundVerifier) Recover(payload, _ []byte) (vo.Address, error) {
	if !bytes.Equal(payload, v.signed) {
		return "", shared.ErrUnauthorized
	}
	return v.address, nil
}

type signedRequest struct {
	timestamp string
	requestID string
	signature string
	body      []byte
}

func newSignedRequest() signedRequest {
	return signedRequest{
		timestamp: now.Format(time.RFC3339),
		requestID: "req-1",
		signature: hex.EncodeToString([]byte("signature")),
		body:      []byte(`{"shard_id":"7"}`),
	}
}

func (s signedRequest) build() *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(s.body))
	r.Header.Set(contract.HeaderTimestamp, s.timestamp)
	r.Header.Set(contract.HeaderRequestID, s.requestID)
	r.Header.Set(contract.HeaderSignature, s.signature)
	return r
}

func TestBoundaryEstablishesWhoIsCallingAndLeavesTheBodyReadable(t *testing.T) {

	verifier := &verifierStub{address: "gonka1creator"}
	boundary := signedhttp.New(verifier, timex.NewFrozen(now), window, audience)
	request := newSignedRequest()
	recorder := httptest.NewRecorder()

	var caller vo.Address
	var seen contract.Command
	handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		caller = signedhttp.AddressFrom(r.Context())
		_ = json.NewDecoder(r.Body).Decode(&seen)
	})

	boundary.Wrap(handler).ServeHTTP(recorder, request.build())

	if recorder.Code != http.StatusOK {
		t.Fatalf("got %d, want the request accepted: %s", recorder.Code, recorder.Body)
	}
	if caller != verifier.address {
		t.Fatalf("got caller %q, want the recovered address", caller)
	}
	if seen.ShardID != "7" {
		t.Fatalf("got body %+v, want the handler to still read it", seen)
	}
	want := contract.SigningPayload(string(audience), http.MethodPost, path, "", request.timestamp, request.requestID, request.body)
	if !bytes.Equal(verifier.payload, want) {
		t.Fatalf("the payload must be built the way the coordinator builds it:\ngot  %q\nwant %q", verifier.payload, want)
	}
}

// A node id is local to a host, so the same signed request replayed elsewhere would name that
// host's node of the same name
func TestGuardRefusesARequestSignedForAnotherHost(t *testing.T) {
	// arrange
	request := newSignedRequest()
	elsewhere := contract.SigningPayload("gonka1elsewhere", http.MethodPost, path, "", request.timestamp, request.requestID, request.body)
	verifier := &boundVerifier{signed: elsewhere, address: "gonka1creator"}
	boundary := signedhttp.New(verifier, timex.NewFrozen(now), window, audience)
	reached := false
	recorder := httptest.NewRecorder()

	// act
	boundary.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })).
		ServeHTTP(recorder, request.build())

	// assert
	if reached {
		t.Fatal("a request signed for another host must never reach the handler")
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want the request turned away: %s", recorder.Code, recorder.Body)
	}
}

func TestGuardCoversTheQueryString(t *testing.T) {
	// arrange
	request := newSignedRequest()
	verifier := &verifierStub{address: "gonka1creator"}
	boundary := signedhttp.New(verifier, timex.NewFrozen(now), window, audience)
	built := request.build()
	built.URL.RawQuery = "tail=100"

	// act
	boundary.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), built)

	// assert
	want := contract.SigningPayload(string(audience), http.MethodPost, path, "tail=100", request.timestamp, request.requestID, request.body)
	if !bytes.Equal(verifier.payload, want) {
		t.Fatalf("a parameter outside the signature is a parameter nobody vouched for:\ngot  %q\nwant %q", verifier.payload, want)
	}
}

func TestGuardRefusesWhatItCannotTrust(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*signedRequest, *verifierStub)
		wantStatus int
		wantCode   string
	}{
		{
			name:       "timestamp older than the window",
			mutate:     func(s *signedRequest, _ *verifierStub) { s.timestamp = now.Add(-2 * window).Format(time.RFC3339) },
			wantStatus: http.StatusUnauthorized,
			wantCode:   "STALE_REQUEST",
		},
		{
			name:       "timestamp from the future",
			mutate:     func(s *signedRequest, _ *verifierStub) { s.timestamp = now.Add(2 * window).Format(time.RFC3339) },
			wantStatus: http.StatusUnauthorized,
			wantCode:   "STALE_REQUEST",
		},
		{
			name:       "no timestamp",
			mutate:     func(s *signedRequest, _ *verifierStub) { s.timestamp = "" },
			wantStatus: http.StatusUnauthorized,
			wantCode:   "STALE_REQUEST",
		},
		{
			name:       "signature that is not hex",
			mutate:     func(s *signedRequest, _ *verifierStub) { s.signature = "not-hex" },
			wantStatus: http.StatusUnauthorized,
			wantCode:   "BAD_SIGNATURE",
		},
		{
			name:       "no signature",
			mutate:     func(s *signedRequest, _ *verifierStub) { s.signature = "" },
			wantStatus: http.StatusUnauthorized,
			wantCode:   "BAD_SIGNATURE",
		},
		{
			name:       "signature that does not verify",
			mutate:     func(_ *signedRequest, v *verifierStub) { v.err = shared.ErrUnauthorized },
			wantStatus: http.StatusUnauthorized,
			wantCode:   "BAD_SIGNATURE",
		},
		{
			name:       "no request id",
			mutate:     func(s *signedRequest, _ *verifierStub) { s.requestID = "" },
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "VALIDATION_ERROR",
		},
		{
			name:       "body over the limit",
			mutate:     func(s *signedRequest, _ *verifierStub) { s.body = bytes.Repeat([]byte("x"), (1<<20)+1) },
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "BAD_BODY",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			verifier := &verifierStub{address: "gonka1creator"}
			request := newSignedRequest()
			tc.mutate(&request, verifier)
			boundary := signedhttp.New(verifier, timex.NewFrozen(now), window, audience)
			reached := false
			handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
			recorder := httptest.NewRecorder()

			boundary.Wrap(handler).ServeHTTP(recorder, request.build())

			if reached {
				t.Fatal("an untrusted request must never reach the handler")
			}
			if recorder.Code != tc.wantStatus {
				t.Fatalf("got %d, want %d: %s", recorder.Code, tc.wantStatus, recorder.Body)
			}
			var envelope contract.Envelope
			if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if envelope.OK || envelope.Error == nil || envelope.Error.Code != tc.wantCode {
				t.Fatalf("got %+v, want error %s", envelope.Error, tc.wantCode)
			}
		})
	}
}
