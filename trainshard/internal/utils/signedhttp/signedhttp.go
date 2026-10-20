package signedhttp

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"time"

	"trainshard/internal/contract"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/httpx"
)

const maxBodyBytes = 1 << 20

const bodyReadTimeout = 30 * time.Second

type contextKey int

const (
	addressKey contextKey = iota
	requestIDKey
	verifiesUntilKey
)

var (
	errBadSignature  = shared.New("BAD_SIGNATURE", shared.ErrUnauthorized, "request signature does not verify")
	errStaleRequest  = shared.New("STALE_REQUEST", shared.ErrUnauthorized, "request timestamp is outside the accepted window")
	errBadBody       = shared.New("BAD_BODY", shared.ErrValidation, "cannot read request body")
	errUnboundedBody = shared.New("BODY_UNBOUNDED", shared.ErrUnavailable, "this server cannot bound how long a request body is read")
)

type Guard struct {
	verifier ports.Verifier
	clock    ports.Clock
	window   time.Duration
	audience vo.Address
	endpoint vo.Endpoint
}

// endpoint is the one this daemon publishes on chain, the coordinator signs the one it took from there
func New(verifier ports.Verifier, clock ports.Clock, window time.Duration, audience vo.Address, endpoint vo.Endpoint) *Guard {
	return &Guard{verifier: verifier, clock: clock, window: window, audience: audience, endpoint: endpoint}
}

func (g *Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID, err := vo.ParseRequestID(r.Header.Get(contract.HeaderRequestID))
		if err != nil {
			httpx.WriteError(w, "", err)
			return
		}
		address, body, verifiesUntil, err := g.authenticate(w, r, requestID)
		if err != nil {
			httpx.WriteError(w, string(requestID), err)
			return
		}

		r.Body = io.NopCloser(bytes.NewReader(body))
		ctx := context.WithValue(r.Context(), addressKey, address)
		ctx = context.WithValue(ctx, requestIDKey, string(requestID))
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, verifiesUntilKey, verifiesUntil)))
	})
}

func (g *Guard) authenticate(w http.ResponseWriter, r *http.Request, requestID vo.RequestID) (vo.Address, []byte, time.Time, error) {
	body, err := readBody(w, r)
	if err != nil {
		return "", nil, time.Time{}, err
	}

	timestamp := r.Header.Get(contract.HeaderTimestamp)
	verifiesUntil, err := g.fresh(timestamp)
	if err != nil {
		return "", nil, time.Time{}, err
	}
	signature, err := hex.DecodeString(r.Header.Get(contract.HeaderSignature))
	if err != nil || len(signature) == 0 {
		return "", nil, time.Time{}, errBadSignature
	}

	payload := contract.SigningPayload(string(g.audience), string(g.endpoint), r.Method, r.URL.Path, r.URL.RawQuery, timestamp, string(requestID), body)
	address, err := g.verifier.Recover(payload, signature)
	if err != nil {
		return "", nil, time.Time{}, errBadSignature
	}
	return address, body, verifiesUntil, nil
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	conn := http.NewResponseController(w)
	if err := conn.SetReadDeadline(time.Now().Add(bodyReadTimeout)); err != nil {
		return nil, errUnboundedBody
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if lifted := conn.SetReadDeadline(time.Time{}); lifted != nil {
		return nil, errUnboundedBody
	}
	if err != nil || len(body) > maxBodyBytes {
		return nil, errBadBody
	}
	return body, nil
}

// the instant returned is the last one this timestamp passes: a spent request id must be kept that long
func (g *Guard) fresh(timestamp string) (time.Time, error) {
	at, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return time.Time{}, errStaleRequest
	}
	drift := g.clock.Now().Sub(at)
	if drift < -g.window || drift > g.window {
		return time.Time{}, errStaleRequest
	}
	return at.Add(g.window), nil
}

func AddressFrom(ctx context.Context) vo.Address {
	address, _ := ctx.Value(addressKey).(vo.Address)
	return address
}

func RequestIDFrom(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDKey).(string)
	return requestID
}

func VerifiesUntilFrom(ctx context.Context) time.Time {
	until, _ := ctx.Value(verifiesUntilKey).(time.Time)
	return until
}
