package poc

import (
	"bytes"
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"common/httpguard"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/mlnodeclient"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type challengeQueryServer struct {
	types.UnimplementedQueryServer
	urls map[string]string
}

func (s *challengeQueryServer) Params(context.Context, *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	return &types.QueryParamsResponse{Params: types.Params{PocParams: &types.PocParams{ValidationSampleSize: 1}}}, nil
}

func (s *challengeQueryServer) PocStageRecipe(context.Context, *types.QueryPocStageRecipeRequest) (*types.QueryPocStageRecipeResponse, error) {
	return &types.QueryPocStageRecipeResponse{Found: true, Recipe: &types.PocStageRecipe{}}, nil
}

func (s *challengeQueryServer) Participant(_ context.Context, req *types.QueryGetParticipantRequest) (*types.QueryGetParticipantResponse, error) {
	return &types.QueryGetParticipantResponse{Participant: types.Participant{
		Index: req.Index, Address: req.Index, InferenceUrl: s.urls[req.Index],
	}}, nil
}

func (s *challengeQueryServer) AccountByAddress(context.Context, *types.QueryAccountByAddressRequest) (*types.QueryAccountByAddressResponse, error) {
	return &types.QueryAccountByAddressResponse{Pubkey: base64.StdEncoding.EncodeToString([]byte{2, 3, 4})}, nil
}

func newChallengeQueryClient(t *testing.T, server types.QueryServer) types.QueryClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	types.RegisterQueryServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = conn.Close()
		grpcServer.Stop()
		_ = listener.Close()
	})
	return types.NewQueryClient(conn)
}

type challengeNodeBroker struct {
	nodes []broker.NodeResponse
}

func (b *challengeNodeBroker) NewNodeClient(*broker.Node) mlnodeclient.MLNodeClient {
	return mlnodeclient.NewMockClient()
}

func (b *challengeNodeBroker) GetNodes() ([]broker.NodeResponse, error) { return b.nodes, nil }

func setChallengeTrackerHeight(tracker *chainphase.ChainPhaseTracker, height int64) {
	epoch := &types.Epoch{Index: 1, PocStartBlockHeight: 100}
	params := &types.EpochParams{
		EpochLength:           1000,
		PocStageDuration:      100,
		PocValidationDelay:    10,
		PocValidationDuration: 100,
	}
	tracker.Update(chainphase.BlockInfo{Height: height, Hash: "fresh-hash"}, epoch, params, true, nil)
}

func punishableChallenge(target string) *types.OpenPoCChallenge {
	return &types.OpenPoCChallenge{
		Challenge: &types.PoCChallenge{Target: target, StartHeight: 10, Seed: []byte{9}},
		Finish:    220,
		Commits: []*types.PoCV2StoreCommit{{
			ParticipantAddress:       target,
			PocStageStartBlockHeight: 10,
			ModelId:                  "model-a",
			Count:                    10,
			RootHash:                 make([]byte, 32),
			TreeDepth:                1,
		}},
	}
}

func TestValidateOpenChallengesSlowTargetDoesNotStarveLaterTargetVote(t *testing.T) {
	t.Cleanup(OpenChallenges.Reset)
	allowPrivate := httpguard.AllowPrivate()
	httpguard.SetAllowPrivate(true)
	t.Cleanup(func() { httpguard.SetAllowPrivate(allowPrivate) })

	var slowHits atomic.Int32
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowHits.Add(1)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(slow.Close)
	later := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"proofs":[]}`))
	}))
	t.Cleanup(later.Close)

	slowTarget := sdk.AccAddress(bytes.Repeat([]byte{0x01}, 20)).String()
	laterTarget := sdk.AccAddress(bytes.Repeat([]byte{0xfe}, 20)).String()
	OpenChallenges.Replace("validator", []*types.OpenPoCChallenge{
		punishableChallenge(slowTarget),
		punishableChallenge(laterTarget),
	}, 200)

	queryClient := newChallengeQueryClient(t, &challengeQueryServer{urls: map[string]string{
		slowTarget:  slow.URL,
		laterTarget: later.URL,
	}})
	votes := make(chan *types.MsgSubmitPoCChallengeValidations, 4)
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("NewInferenceQueryClient").Return(queryClient)
	recorder.On("GetAccountAddress").Return("validator")
	recorder.On("GetSignerAddress").Return("validator-signer")
	recorder.On("SignBytes", mock.Anything).Return([]byte("signature"), nil)
	recorder.On("SubmitPoCChallengeValidations", mock.Anything).Run(func(args mock.Arguments) {
		votes <- args.Get(0).(*types.MsgSubmitPoCChallengeValidations)
	}).Return(nil)

	tracker := &chainphase.ChainPhaseTracker{}
	setChallengeTrackerHeight(tracker, 220)
	v := &OffChainValidator{
		recorder: recorder,
		nodeBroker: &challengeNodeBroker{nodes: []broker.NodeResponse{{
			Node: broker.Node{Id: "node", Models: map[string]broker.ModelArgs{"model-a": {}}},
			State: broker.NodeState{
				CurrentStatus: types.HardwareNodeStatus_POC,
				AdminState:    broker.AdminState{Enabled: true},
			},
		}}},
		phaseTracker: tracker,
		pubKey:       "validator-pubkey",
		config: ValidationConfig{
			WorkerCount:        1,
			RequestTimeout:     time.Minute,
			MaxRetries:         25,
			RetryBackoff:       10 * time.Millisecond,
			PhaseCheckInterval: 5 * time.Millisecond,
		},
	}

	done := make(chan struct{})
	go func() {
		v.ValidateOpenChallenges()
		close(done)
	}()
	t.Cleanup(func() {
		setChallengeTrackerHeight(tracker, 500)
		close(release)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("challenge validation did not stop during cleanup")
		}
	})

	select {
	case vote := <-votes:
		require.Equal(t, int64(10), vote.PocStageStartBlockHeight)
		require.Len(t, vote.Validations, 1)
		require.Equal(t, laterTarget, vote.Validations[0].ParticipantAddress)
		require.Equal(t, "model-a", vote.Validations[0].ModelId)
		require.Equal(t, int64(-1), vote.Validations[0].ValidatedWeight)
	case <-time.After(2 * time.Second):
		t.Fatal("later challenge target received no vote while the first target was blocked")
	}
	require.Equal(t, int32(1), slowHits.Load())

	select {
	case <-done:
		t.Fatal("challenge validation returned while the vote window was open")
	default:
	}

	setChallengeTrackerHeight(tracker, 500)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("challenge validation did not return after the vote window closed")
	}
	require.Empty(t, votes)
}
