package ports

import (
	"context"
	"time"

	"trainshard/internal/domain/shared/vo"
)

// Clock is the only now
type Clock interface {
	// Now returns wall time
	Now() time.Time
}

// Attestor signs via dAPI
type Attestor interface {
	// Attest returns a signature over the payload
	Attest(ctx context.Context, payload []byte) ([]byte, error)
}

// Verifier recovers who signed
type Verifier interface {
	// Recover returns the address, or error if it doesn't verify
	Recover(payload, signature []byte) (vo.Address, error)
}

// Probe host checks before a node is leased
type Probe interface {
	// GPUContainer starts a throwaway GPU box; error if the runtime can't
	GPUContainer(ctx context.Context) error
	// FreeDiskBytes returns free disk for run volumes
	FreeDiskBytes(ctx context.Context) (int64, error)
	// MeshPortReachable error unless the endpoint is routable and the port is free; only a peer proves the rest
	MeshPortReachable(ctx context.Context) error
}
