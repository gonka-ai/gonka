// Package nmclient is the unary NodeManager consumer of GetBlockHeader
// and ProveBlockPath. Live tip is Comet NewBlock, not a gRPC stream.
package nmclient

import (
	"context"
	"errors"
	"fmt"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/nmrpc"
	"common/nodemanager/gen"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Lookup implements blocks.BlockOracle over a NodeManager stub.
type Lookup struct {
	client gen.NodeManagerClient
}

// New returns a unary lookup. client must be non-nil.
func New(client gen.NodeManagerClient) (*Lookup, error) {
	if client == nil {
		return nil, errors.New("nmclient: nil NodeManager client")
	}
	return &Lookup{client: client}, nil
}

func (l *Lookup) Latest(ctx context.Context) (*blocks.Header, error) {
	return l.header(ctx, 0)
}

func (l *Lookup) At(ctx context.Context, height int64) (*blocks.Header, error) {
	if height <= 0 {
		return nil, fmt.Errorf("nmclient: At: %w", blocks.ErrHeaderNotFound)
	}
	return l.header(ctx, height)
}

func (l *Lookup) header(ctx context.Context, height int64) (*blocks.Header, error) {
	if l == nil || l.client == nil {
		return nil, blocks.ErrHeaderNotFound
	}
	resp, err := l.client.GetBlockHeader(ctx, &gen.GetBlockHeaderRequest{Height: height})
	if err != nil {
		return nil, mapHeaderErr(err)
	}
	h := nmrpc.HeaderFromProto(resp.GetHeader())
	if h == nil {
		return nil, blocks.ErrHeaderNotFound
	}
	return h, nil
}

func (l *Lookup) Prove(ctx context.Context, path string, height int64) (*blocks.Proof, error) {
	if l == nil || l.client == nil {
		return nil, blocks.ErrProveNotImplemented
	}
	resp, err := l.client.ProveBlockPath(ctx, &gen.ProveBlockPathRequest{Height: height, Path: path})
	if err != nil {
		return nil, mapProveErr(err)
	}
	p := nmrpc.ProofFromProto(resp.GetProof())
	if p == nil {
		return nil, blocks.ErrHeaderNotFound
	}
	return p, nil
}

func (l *Lookup) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	return nil, errors.New("nmclient: no subscribe; use the Comet tip")
}

// mapHeaderErr keeps a missing height, a dapi without the RPC, and a dapi
// with no oracle configured on the same quiet path HTTP 404 on /block/* takes.
// Transport and other codes stay verbatim so failover can tell them apart.
func mapHeaderErr(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch st.Code() {
	case codes.NotFound, codes.Unimplemented, codes.FailedPrecondition:
		return fmt.Errorf("%w: %s", blocks.ErrHeaderNotFound, st.Message())
	default:
		return err
	}
}

// mapProveErr differs from mapHeaderErr on Unimplemented: to a Prove caller a
// hash-only oracle and a dapi missing the RPC both mean "no proof available",
// which is what ErrProveNotImplemented already says. Matching on the status
// message instead would break the moment a producer reworded it.
func mapProveErr(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch st.Code() {
	case codes.Unimplemented:
		return blocks.ErrProveNotImplemented
	case codes.NotFound, codes.FailedPrecondition:
		return fmt.Errorf("%w: %s", blocks.ErrHeaderNotFound, st.Message())
	default:
		return err
	}
}

var _ blocks.BlockOracle = (*Lookup)(nil)
