package run

import (
	"context"
	"time"

	"trainshard/internal/domain/shared/vo"
)

type Desired struct {
	Reservation
	Reserved bool
	// Shard is then the one being cleaned up, while the chain already reserves the node for another
	Handover       bool
	MeshConfigured bool
	Run            RunSpec
	Revision       int
	Start          bool
	StopGrace      time.Duration
	StopGraceGiven bool
}

func DesiredFor(reservation Reservation, state RunState, meshConfigured bool) Desired {
	return Desired{
		Reservation:    reservation,
		Reserved:       true,
		MeshConfigured: meshConfigured,
		Run:            state.Spec,
		Revision:       state.Revision,
		Start:          state.Start,
		StopGrace:      state.StopGrace,
		StopGraceGiven: state.StopGraceGiven,
	}
}

func ReadDesired(ctx context.Context, chain Reservations, network RunNetwork, node vo.NodeRef, state RunState) (Desired, error) {
	reservation, reserved, err := chain.Reserved(ctx, node)
	if err != nil {
		return Desired{}, err
	}
	if !reserved {
		return Desired{Reservation: Reservation{Shard: state.Shard}}, nil
	}
	if !state.Shard.IsZero() && state.Shard != reservation.Shard {
		return Desired{Reservation: Reservation{Shard: state.Shard}, Handover: true}, nil
	}

	configured, err := network.Configured(ctx, reservation.Shard, node)
	if err != nil {
		return Desired{}, err
	}
	return DesiredFor(reservation, state, configured), nil
}
