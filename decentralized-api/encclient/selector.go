package encclient

import (
	"context"
	"fmt"

	"github.com/cosmos/cosmos-sdk/types/query"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
)

type ActiveAttestation struct {
	ParticipantAddress string
	NodeID             string
	PublicKey          []byte
	Provider           string
	Measurement        string
	EnvelopeVersion    string
	QuoteHash          []byte
	AttestedAtHeight   int64
	ExpiresAtHeight    int64
}

type QueryClient interface {
	TeeAttestationsVerified(
		ctx context.Context,
		in *inferencetypes.QueryTeeAttestationsVerifiedRequest,
		opts ...grpc.CallOption,
	) (*inferencetypes.QueryTeeAttestationsVerifiedResponse, error)
}

type SelectorOptions struct {
	Provider string

	PageSize uint64
}

const defaultPageSize uint64 = 200

type Selector struct {
	qc      QueryClient
	options SelectorOptions
}

func NewSelector(qc QueryClient, opts SelectorOptions) *Selector {
	if opts.PageSize == 0 {
		opts.PageSize = defaultPageSize
	}
	return &Selector{qc: qc, options: opts}
}

func (s *Selector) ActiveAttestations(ctx context.Context) ([]ActiveAttestation, error) {
	var (
		out     []ActiveAttestation
		nextKey []byte
	)
	for {
		resp, err := s.qc.TeeAttestationsVerified(ctx, &inferencetypes.QueryTeeAttestationsVerifiedRequest{
			Provider: s.options.Provider,
			Pagination: &query.PageRequest{
				Limit: s.options.PageSize,
				Key:   nextKey,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("encclient: query TeeAttestationsVerified: %w", err)
		}

		for _, att := range resp.GetAttestations() {
			if att == nil || len(att.PublicKey) != X25519PublicKeySize {
				continue
			}
			out = append(out, ActiveAttestation{
				ParticipantAddress: att.ParticipantAddress,
				NodeID:             att.NodeId,
				PublicKey:          append([]byte(nil), att.PublicKey...),
				Provider:           att.Provider,
				Measurement:        att.Measurement,
				EnvelopeVersion:    att.EnvelopeVersion,
				QuoteHash:          append([]byte(nil), att.QuoteHash...),
				AttestedAtHeight:   att.AttestedAtHeight,
				ExpiresAtHeight:    att.ExpiresAtHeight,
			})
		}

		if resp.GetPagination() == nil || len(resp.GetPagination().NextKey) == 0 {
			return out, nil
		}
		nextKey = resp.GetPagination().NextKey
	}
}
