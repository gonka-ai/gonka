package chain

import (
	"context"
	"time"
)

const (
	participantsCacheMaxAge      = 10 * time.Minute
	participantsUnverifiedMaxAge = time.Minute
	participantsRefetchNow       = time.Duration(0)
	effectiveEpochUnknown        = uint64(0)
)

type participantsCache struct {
	payload   chainCurrentParticipantsResponse
	epoch     uint64
	fetchedAt time.Time
	maxAge    time.Duration
	held      bool
}

func (c *participantsCache) holds(epoch uint64, now time.Time) bool {
	return c.held && c.epoch == epoch && now.Sub(c.fetchedAt) < c.maxAge
}

func (c *participantsCache) store(payload chainCurrentParticipantsResponse, epoch uint64, now time.Time) {
	c.payload, c.epoch, c.fetchedAt, c.held = payload, epoch, now, true
	switch reported := payload.ActiveParticipants.EpochID; {
	case reported == nil:
		c.maxAge = participantsUnverifiedMaxAge
	case uint64(*reported) < epoch:
		c.maxAge = participantsRefetchNow
	default:
		c.maxAge = participantsCacheMaxAge
	}
}

func participantsEpoch(epoch epochInfo) uint64 {
	if epoch.EffectiveEpochIndex == effectiveEpochUnknown {
		return epoch.EpochIndex
	}
	return epoch.EffectiveEpochIndex
}

func (o *PhaseObserver) participantsFor(ctx context.Context, effectiveEpoch uint64) (chainCurrentParticipantsResponse, error) {
	if o.participants.holds(effectiveEpoch, o.now()) {
		return o.participants.payload, nil
	}
	payload, err := o.fetchParticipants(ctx)
	if err != nil {
		return chainCurrentParticipantsResponse{}, err
	}
	o.participants.store(payload, effectiveEpoch, o.now())
	return payload, nil
}
