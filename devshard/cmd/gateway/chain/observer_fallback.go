package chain

import (
	"context"
	"fmt"
)

// payloadFallback is the chain client's answer to the two reads the public API serves, used when that API cannot answer. See README.md, "The poll loop".
type payloadFallback interface {
	epochInfoPayload(ctx context.Context) (chainEpochInfoResponse, error)
	participantsPayload(ctx context.Context) (chainCurrentParticipantsResponse, error)
}

// chainReadContext gives one chain client read its own deadline, so a read that hangs cannot take the rest of the poll with it.
func (o *PhaseObserver) chainReadContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, o.chainReadTimeout)
}

func (o *PhaseObserver) fetchEpochInfo(ctx context.Context) (epochInfo, error) {
	info, apiErr := o.fetchEpochInfoFromAPI(ctx)
	if apiErr == nil || o.fallback == nil {
		return info, apiErr
	}
	readContext, cancel := o.chainReadContext(ctx)
	defer cancel()
	payload, chainErr := o.fallback.epochInfoPayload(readContext)
	if chainErr != nil {
		return epochInfo{}, fmt.Errorf("public API: %w; chain fallback: %w", apiErr, chainErr)
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
	payload, apiErr := o.fetchParticipantsFromAPI(ctx)
	if apiErr == nil || o.fallback == nil {
		return payload, apiErr
	}
	readContext, cancel := o.chainReadContext(ctx)
	defer cancel()
	payload, chainErr := o.fallback.participantsPayload(readContext)
	if chainErr != nil {
		return chainCurrentParticipantsResponse{}, fmt.Errorf("public API: %w; chain fallback: %w", apiErr, chainErr)
	}
	return payload, nil
}

func (o *PhaseObserver) fetchParticipantsFromAPI(ctx context.Context) (chainCurrentParticipantsResponse, error) {
	body, err := o.getBody(ctx, participantsPath)
	if err != nil {
		return chainCurrentParticipantsResponse{}, err
	}
	return decodeParticipants(body)
}
