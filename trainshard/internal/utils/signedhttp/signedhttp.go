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

type contextKey int

const (
	addressKey contextKey = iota
	requestIDKey
)

var (
	errBadSignature = shared.New("BAD_SIGNATURE", shared.ErrUnauthorized, "request signature does not verify")
	errStaleRequest = shared.New("STALE_REQUEST", shared.ErrUnauthorized, "request timestamp is outside the accepted window")
	errBadBody      = shared.New("BAD_BODY", shared.ErrValidation, "cannot read request body")
)

type Guard struct {
	verifier ports.Verifier
	clock    ports.Clock
	window   time.Duration
	audience vo.Address
}

func New(verifier ports.Verifier, clock ports.Clock, window time.Duration, audience vo.Address) *Guard {
	return &Guard{verifier: verifier, clock: clock, window: window, audience: audience}
}

func (g *Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get(contract.HeaderRequestID)
		address, body, err := g.authenticate(r)
		if err != nil {
			httpx.WriteError(w, requestID, err)
			return
		}

		r.Body = io.NopCloser(bytes.NewReader(body))
		ctx := context.WithValue(r.Context(), addressKey, address)
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, requestIDKey, requestID)))
	})
}

func (g *Guard) authenticate(r *http.Request) (vo.Address, []byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		return "", nil, errBadBody
	}

	requestID, err := vo.ParseRequestID(r.Header.Get(contract.HeaderRequestID))
	if err != nil {
		return "", nil, err
	}
	timestamp := r.Header.Get(contract.HeaderTimestamp)
	if err := g.fresh(timestamp); err != nil {
		return "", nil, err
	}
	signature, err := hex.DecodeString(r.Header.Get(contract.HeaderSignature))
	if err != nil || len(signature) == 0 {
		return "", nil, errBadSignature
	}

	payload := contract.SigningPayload(string(g.audience), r.Method, r.URL.Path, r.URL.RawQuery, timestamp, string(requestID), body)
	address, err := g.verifier.Recover(payload, signature)
	if err != nil {
		return "", nil, errBadSignature
	}
	return address, body, nil
}

func (g *Guard) fresh(timestamp string) error {
	at, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return errStaleRequest
	}
	drift := g.clock.Now().Sub(at)
	if drift < -g.window || drift > g.window {
		return errStaleRequest
	}
	return nil
}

func AddressFrom(ctx context.Context) vo.Address {
	address, _ := ctx.Value(addressKey).(vo.Address)
	return address
}

func RequestIDFrom(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDKey).(string)
	return requestID
}
