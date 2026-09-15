package run

import (
	"context"
	"time"

	"trainshard/internal/domain/shared/vo"
)

type Desired struct {
	Reservation
	Reserved       bool
	MeshConfigured bool
	Run            RunSpec
	Revision       int
	Start          bool
	StopGrace      time.Duration
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
	}
}

func ReadDesired(ctx context.Context, chain Reservations, network RunNetwork, node vo.NodeRef, state RunState) (Desired, error) {
	reservation, reserved, err := chain.Reserved(ctx, node)
	if err != nil {
		return Desired{}, err
	}
	if !reserved || (!state.Shard.IsZero() && state.Shard != reservation.Shard) {
		return Desired{Reservation: Reservation{Shard: state.Shard}}, nil
	}

	configured, err := network.Configured(ctx, reservation.Shard, node)
	if err != nil {
		return Desired{}, err
	}
	return DesiredFor(reservation, state, configured), nil
}
