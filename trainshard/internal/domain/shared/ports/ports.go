package ports

import (
	"context"
	"time"

	"trainshard/internal/domain/shared/vo"
)

// Clock is the only source of now; nothing outside its adapter reads the wall clock
type Clock interface {
	// Now returns the current wall time
	Now() time.Time
}

// Attestor signs what the daemon says for itself with the participant's own key
type Attestor interface {
	// Attest returns a signature over the payload, which Verifier.Recover turns back into the
	// participant's address; an error means nothing was signed
	Attest(ctx context.Context, payload []byte) ([]byte, error)
}

// Verifier recovers who signed a payload
type Verifier interface {
	// Recover returns the address that signed the payload; an error means the signature does not
	// verify, never an empty address
	Recover(payload, signature []byte) (vo.Address, error)
}

// Probe is the machine's self-checks before a node is opted in; each error is the reason the node
// is not ready
type Probe interface {
	// GPUContainer starts and removes a throwaway container with the gpus; an error if the runtime
	// cannot, except that ErrUnavailable says nothing about the cards and the last verdict stands
	GPUContainer(ctx context.Context) error
	// FreeDiskBytes returns the free disk left for run volumes
	FreeDiskBytes(ctx context.Context) (int64, error)
	// MeshPortReachable returns an error unless the mesh endpoint is routable and the port is free;
	// that it is reachable from outside only a peer can prove
	MeshPortReachable(ctx context.Context) error
}

// Delegation says who may speak for a participant
type Delegation interface {
	// Speaks returns whether the signer is the participant itself or holds its warm key grant on
	// chain; an error means the chain could not be asked, never a refusal
	Speaks(ctx context.Context, participant vo.Participant, signer vo.Address) (bool, error)
}
