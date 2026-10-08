package chain

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/timex"
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

func clientOver(stub *granteesStub, clock *timex.Frozen) *Client {
	return &Client{query: stub, clock: clock, grants: grants{answers: map[grantKey]grantAnswer{}}}
}

func frozen() *timex.Frozen {
	return timex.NewFrozen(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
}

func TestTheParticipantSpeaksForItselfWithoutAskingTheChain(t *testing.T) {
	// arrange
	stub := &granteesStub{}
	client := clientOver(stub, frozen())

	// act
	speaks, err := client.Speaks(context.Background(), cold, vo.Address(cold))
	missing, missingErr := client.MissingGrants(context.Background(), cold, vo.Address(cold))

	// assert
	if err != nil || missingErr != nil || !speaks || len(missing) != 0 || stub.asked != 0 {
		t.Fatalf("speaks=%v missing=%v asked=%d errs=%v %v", speaks, missing, stub.asked, err, missingErr)
	}
}

func TestAWarmKeySpeaksOnceTheMarkerGrantIsOnChain(t *testing.T) {
	// arrange
	stub := &granteesStub{granted: map[string][]string{
		string(cold) + " " + types.WarmKeyGrantMarkerTypeURL: {string(warm)},
	}}
	client := clientOver(stub, frozen())

	// act
	speaks, err := client.Speaks(context.Background(), cold, warm)
	again, _ := client.Speaks(context.Background(), cold, warm)
	stranger, _ := client.Speaks(context.Background(), cold, "gonka1stranger")

	// assert
	if err != nil || !speaks || !again || stranger {
		t.Fatalf("speaks=%v again=%v stranger=%v err=%v", speaks, again, stranger, err)
	}
	if stub.asked != 2 {
		t.Fatalf("asked the chain %d times, want the granted answer kept", stub.asked)
	}
}

func TestAGrantAnswerIsAskedAgainOnceItGoesStale(t *testing.T) {
	cases := []struct {
		name    string
		granted []string
		after   time.Duration
		asked   int
	}{
		{"a refusal inside its keep", nil, refusalKeeps, 1},
		{"a refusal past its keep", nil, refusalKeeps + time.Second, 2},
		{"a grant inside its keep", []string{string(warm)}, grantKeeps, 1},
		{"a grant past its keep", []string{string(warm)}, grantKeeps + time.Second, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			stub := &granteesStub{granted: map[string][]string{string(cold) + " " + types.WarmKeyGrantMarkerTypeURL: tc.granted}}
			clock := frozen()
			client := clientOver(stub, clock)
			if _, err := client.Speaks(context.Background(), cold, warm); err != nil {
				t.Fatal(err)
			}
			clock.Advance(tc.after - time.Nanosecond)

			// act
			_, err := client.Speaks(context.Background(), cold, warm)

			// assert
			if err != nil {
				t.Fatal(err)
			}
			if stub.asked != tc.asked {
				t.Fatalf("asked the chain %d times, want %d", stub.asked, tc.asked)
			}
		})
	}
}

func TestMissingGrantsNamesWhatTrainingStillLacks(t *testing.T) {
	// arrange
	stub := &granteesStub{granted: map[string][]string{
		string(cold) + " " + types.WarmKeyGrantMarkerTypeURL: {string(warm)},
		string(cold) + " " + trainingGrants[1]:               {string(warm)},
	}}
	client := clientOver(stub, frozen())

	// act
	missing, err := client.MissingGrants(context.Background(), cold, warm)

	// assert
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != trainingGrants[2] {
		t.Fatalf("missing = %v, want only the autokick grant", missing)
	}
}

func TestMissingGrantsReportsAChainThatCannotBeAskedAsUnavailable(t *testing.T) {
	// arrange
	client := clientOver(&granteesStub{err: errors.New("chain down")}, frozen())

	// act
	_, err := client.MissingGrants(context.Background(), cold, warm)

	// assert
	if !errors.Is(err, shared.ErrUnavailable) {
		t.Fatalf("got %v, want the chain error surfaced as unavailable", err)
	}
}
