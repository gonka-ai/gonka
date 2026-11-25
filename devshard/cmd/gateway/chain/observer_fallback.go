package chain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const publicAPIThrottleBackoff = 5 * time.Minute

// payloadFallback is the chain client's answer to the two reads the public API serves, used when that API cannot answer. See README.md, "The poll loop".
type payloadFallback interface {
	epochInfoPayload(ctx context.Context) (chainEpochInfoResponse, error)
	participantsPayload(ctx context.Context) (chainCurrentParticipantsResponse, error)
}

type publicAPIStatusError struct {
	path   string
	status int
}

func (e *publicAPIStatusError) Error() string {
	return fmt.Sprintf("%s status %d", e.path, e.status)
}

func isPublicAPIThrottle(err error) bool {
	var statusErr *publicAPIStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.status == http.StatusTooManyRequests || statusErr.status == http.StatusServiceUnavailable
}

// chainReadContext gives one chain client read its own deadline, so a read that hangs cannot take the rest of the poll with it.
func (o *PhaseObserver) chainReadContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, o.chainReadTimeout)
}

func (o *PhaseObserver) fetchEpochInfo(ctx context.Context) (epochInfo, error) {
	return readPublicAPIOrChain(ctx, o, o.fetchEpochInfoFromAPI, o.fallbackEpochInfo)
}

func (o *PhaseObserver) fallbackEpochInfo(ctx context.Context) (epochInfo, error) {
	payload, err := o.fallback.epochInfoPayload(ctx)
	if err != nil {
		return epochInfo{}, err
	}
	return epochInfoFromPayload(payload), nil
}

func (o *PhaseObserver) fetchEpochInfoFromAPI(ctx context.Context) (epochInfo, error) {
	body, err := o.getBody(ctx, epochInfoPath)
	if err != nil {
		return epochInfo{}, err
	}
	return parseEpochInfo(body)
}

func (o *PhaseObserver) fetchParticipants(ctx context.Context) (chainCurrentParticipantsResponse, error) {
	return readPublicAPIOrChain(ctx, o, o.fetchParticipantsFromAPI, o.fallbackParticipants)
}

func (o *PhaseObserver) fallbackParticipants(ctx context.Context) (chainCurrentParticipantsResponse, error) {
	return o.fallback.participantsPayload(ctx)
}

func (o *PhaseObserver) fetchParticipantsFromAPI(ctx context.Context) (chainCurrentParticipantsResponse, error) {
	body, err := o.getBody(ctx, participantsPath)
	if err != nil {
		return chainCurrentParticipantsResponse{}, err
	}
	return decodeParticipants(body)
}

func readPublicAPIOrChain[T any](ctx context.Context, o *PhaseObserver, fromAPI, fromChain func(context.Context) (T, error)) (T, error) {
	var zero T
	if o.fallback == nil {
		return fromAPI(ctx)
	}
	if o.publicAPIThrottled() {
		if payload, err := readChain(ctx, o, fromChain); err == nil {
			return payload, nil
		}
		o.publicAPIBackoffUntil = time.Time{}
	}
	payload, apiErr := fromAPI(ctx)
	if apiErr == nil {
		return payload, nil
	}
	if isPublicAPIThrottle(apiErr) {
		o.publicAPIBackoffUntil = o.now().Add(publicAPIThrottleBackoff)
	}
	payload, chainErr := readChain(ctx, o, fromChain)
	if chainErr != nil {
		return zero, fmt.Errorf("public API: %w; chain fallback: %w", apiErr, chainErr)
	}
	return payload, nil
}

func readChain[T any](ctx context.Context, o *PhaseObserver, read func(context.Context) (T, error)) (T, error) {
	readContext, cancel := o.chainReadContext(ctx)
	defer cancel()
	return read(readContext)
}

func (o *PhaseObserver) publicAPIThrottled() bool {
	return o.now().Before(o.publicAPIBackoffUntil)
}
