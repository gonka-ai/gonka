package encclient

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/cosmos/cosmos-sdk/types/query"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
)

var _ QueryClient = (inferencetypes.QueryClient)(nil)

type fakeQuery struct {
	pages       []*inferencetypes.QueryTeeAttestationsVerifiedResponse
	calls       []*inferencetypes.QueryTeeAttestationsVerifiedRequest
	returnError error
}

func (f *fakeQuery) TeeAttestationsVerified(
	_ context.Context,
	req *inferencetypes.QueryTeeAttestationsVerifiedRequest,
	_ ...grpc.CallOption,
) (*inferencetypes.QueryTeeAttestationsVerifiedResponse, error) {
	if f.returnError != nil {
		return nil, f.returnError
	}
	f.calls = append(f.calls, req)
	idx := len(f.calls) - 1
	if idx >= len(f.pages) {
		return &inferencetypes.QueryTeeAttestationsVerifiedResponse{}, nil
	}
	return f.pages[idx], nil
}

func mkAtt(addr, node string, pubLen int) *inferencetypes.TeeAttestation {
	return &inferencetypes.TeeAttestation{
		ParticipantAddress: addr,
		NodeId:             node,
		PublicKey:          bytes.Repeat([]byte{0xAB}, pubLen),
		Provider:           "intel-tdx-lite",
		Measurement:        "deadbeef",
		EnvelopeVersion:    "v1",
		QuoteHash:          bytes.Repeat([]byte{0xCD}, 32),
		AttestedAtHeight:   100,
		ExpiresAtHeight:    200,
	}
}

func TestSelectorReturnsAllActiveAttestations(t *testing.T) {
	fq := &fakeQuery{
		pages: []*inferencetypes.QueryTeeAttestationsVerifiedResponse{
			{
				Attestations: []*inferencetypes.TeeAttestation{
					mkAtt("addr1", "node-a", 32),
					mkAtt("addr2", "node-b", 32),
				},
			},
		},
	}
	got, err := NewSelector(fq, SelectorOptions{}).ActiveAttestations(context.Background())
	if err != nil {
		t.Fatalf("ActiveAttestations: %v", err)
	}
	if len(got) != 2 || got[0].ParticipantAddress != "addr1" || got[1].NodeID != "node-b" {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestSelectorSkipsMalformedPublicKey(t *testing.T) {
	fq := &fakeQuery{
		pages: []*inferencetypes.QueryTeeAttestationsVerifiedResponse{
			{
				Attestations: []*inferencetypes.TeeAttestation{
					mkAtt("addr1", "node-a", 32),
					mkAtt("addr2", "node-b", 16),
				},
			},
		},
	}
	got, _ := NewSelector(fq, SelectorOptions{}).ActiveAttestations(context.Background())
	if len(got) != 1 || got[0].NodeID != "node-a" {
		t.Fatalf("expected only node-a, got %+v", got)
	}
}

func TestSelectorFollowsPagination(t *testing.T) {
	fq := &fakeQuery{
		pages: []*inferencetypes.QueryTeeAttestationsVerifiedResponse{
			{
				Attestations: []*inferencetypes.TeeAttestation{mkAtt("a", "1", 32)},
				Pagination:   &query.PageResponse{NextKey: []byte("next")},
			},
			{
				Attestations: []*inferencetypes.TeeAttestation{mkAtt("b", "2", 32)},
			},
		},
	}
	got, err := NewSelector(fq, SelectorOptions{PageSize: 10}).ActiveAttestations(context.Background())
	if err != nil {
		t.Fatalf("ActiveAttestations: %v", err)
	}
	if len(got) != 2 || len(fq.calls) != 2 {
		t.Fatalf("expected 2 items across 2 calls, got %d items / %d calls", len(got), len(fq.calls))
	}
	if string(fq.calls[1].Pagination.Key) != "next" {
		t.Fatalf("second call did not carry next key: %q", fq.calls[1].Pagination.Key)
	}
}

func TestSelectorPropagatesError(t *testing.T) {
	fq := &fakeQuery{returnError: errors.New("boom")}
	if _, err := NewSelector(fq, SelectorOptions{}).ActiveAttestations(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}
