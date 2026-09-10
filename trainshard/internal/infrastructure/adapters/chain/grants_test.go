package chain

import (
	"context"
	"errors"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"

	"trainshard/internal/domain/shared/vo"
)

const (
	cold = vo.Participant("gonka1cold")
	warm = vo.Address("gonka1warm")
)

type granteesStub struct {
	types.QueryClient
	granted map[string][]string
	asked   int
	err     error
}

func (g *granteesStub) GranteesByMessageType(_ context.Context, req *types.QueryGranteesByMessageTypeRequest, _ ...grpc.CallOption) (*types.QueryGranteesByMessageTypeResponse, error) {
	g.asked++
	if g.err != nil {
		return nil, g.err
	}
	reply := &types.QueryGranteesByMessageTypeResponse{}
	for _, address := range g.granted[req.GranterAddress+" "+req.MessageTypeUrl] {
		reply.Grantees = append(reply.Grantees, &types.Grantee{Address: address})
	}
	return reply, nil
}

func clientOver(stub *granteesStub) *Client {
	return &Client{query: stub, grants: grants{answers: map[grantKey]grantAnswer{}}}
}

func TestTheParticipantSpeaksForItselfWithoutAskingTheChain(t *testing.T) {
	stub := &granteesStub{}
	client := clientOver(stub)

	speaks, err := client.Speaks(context.Background(), cold, vo.Address(cold))
	missing, missingErr := client.MissingGrants(context.Background(), cold, vo.Address(cold))

	if err != nil || missingErr != nil || !speaks || len(missing) != 0 || stub.asked != 0 {
		t.Fatalf("speaks=%v missing=%v asked=%d errs=%v %v", speaks, missing, stub.asked, err, missingErr)
	}
}

func TestAWarmKeySpeaksOnceTheMarkerGrantIsOnChain(t *testing.T) {
	stub := &granteesStub{granted: map[string][]string{
		string(cold) + " " + types.WarmKeyGrantMarkerTypeURL: {string(warm)},
	}}
	client := clientOver(stub)

	speaks, err := client.Speaks(context.Background(), cold, warm)
	again, _ := client.Speaks(context.Background(), cold, warm)
	stranger, _ := client.Speaks(context.Background(), cold, "gonka1stranger")

	if err != nil || !speaks || !again || stranger {
		t.Fatalf("speaks=%v again=%v stranger=%v err=%v", speaks, again, stranger, err)
	}
	if stub.asked != 2 {
		t.Fatalf("asked the chain %d times, want the granted answer kept", stub.asked)
	}
}

func TestMissingGrantsNamesWhatTrainingStillLacks(t *testing.T) {
	stub := &granteesStub{granted: map[string][]string{
		string(cold) + " " + types.WarmKeyGrantMarkerTypeURL: {string(warm)},
		string(cold) + " " + trainingGrants[1]:               {string(warm)},
	}}
	client := clientOver(stub)

	missing, err := client.MissingGrants(context.Background(), cold, warm)

	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != trainingGrants[2] {
		t.Fatalf("missing = %v, want only the autokick grant", missing)
	}
}

func TestMissingGrantsReportsAChainThatCannotBeAsked(t *testing.T) {
	client := clientOver(&granteesStub{err: errors.New("chain down")})

	if _, err := client.MissingGrants(context.Background(), cold, warm); err == nil {
		t.Fatal("want the chain error surfaced")
	}
}
