package poc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"decentralized-api/broker"
	"decentralized-api/cosmosclient"
	"decentralized-api/mlnodeclient"
	"decentralized-api/poc/earlyshare"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// stubProofFetcher implements proofFetcher and returns a fixed set of artifacts.
type stubProofFetcher struct {
	artifacts []VerifiedArtifact
	calls     int
}

func (s *stubProofFetcher) FetchAndVerifyProofs(_ context.Context, _ string, _ ProofRequest) ([]VerifiedArtifact, error) {
	s.calls++
	return s.artifacts, nil
}

func (s *stubProofFetcher) FetchAndVerifyProofsByNonce(_ context.Context, _ string, _ ProofByNonceRequest) ([]VerifiedArtifact, error) {
	return s.artifacts, nil
}

type failingProofFetcher struct {
	err error
}

func (f *failingProofFetcher) FetchAndVerifyProofs(_ context.Context, _ string, _ ProofRequest) ([]VerifiedArtifact, error) {
	return nil, f.err
}

func (f *failingProofFetcher) FetchAndVerifyProofsByNonce(_ context.Context, _ string, _ ProofByNonceRequest) ([]VerifiedArtifact, error) {
	return nil, f.err
}

// cancellingProofFetcher cancels the validation context before failing, mimicking the
// phase watcher ending the window while a fetch is in flight.
type cancellingProofFetcher struct {
	cancel context.CancelFunc
	err    error
}

func (f *cancellingProofFetcher) FetchAndVerifyProofs(_ context.Context, _ string, _ ProofRequest) ([]VerifiedArtifact, error) {
	f.cancel()
	return nil, f.err
}

func (f *cancellingProofFetcher) FetchAndVerifyProofsByNonce(_ context.Context, _ string, _ ProofByNonceRequest) ([]VerifiedArtifact, error) {
	f.cancel()
	return nil, f.err
}

// stubNodeBroker implements nodeBrokerFacade for tests.
type stubNodeBroker struct {
	client mlnodeclient.MLNodeClient
}

func (s *stubNodeBroker) NewNodeClient(_ *broker.Node) mlnodeclient.MLNodeClient {
	return s.client
}

func (s *stubNodeBroker) GetNodes() ([]broker.NodeResponse, error) {
	return nil, nil
}

// runValidateParticipant is a helper that runs validateParticipant with fixed
// test fixtures and returns the captured GenerateV2 request.
func runValidateParticipant(t *testing.T, pocStrongerRng bool) *mlnodeclient.PoCGenerateRequestV2 {
	t.Helper()
	mockClient := mlnodeclient.NewMockClient()

	v := &OffChainValidator{
		nodeBroker:  &stubNodeBroker{client: mockClient},
		callbackUrl: "http://callback",
	}

	testNode := broker.NodeResponse{
		Node: broker.Node{
			Host:    "127.0.0.1",
			PoCPort: 8080,
			NodeNum: 1,
			Models: map[string]broker.ModelArgs{
				"test-model": {},
			},
		},
	}

	// Stub returns one artifact; nonce=1, count=100 -> porosity=0.01, well below threshold.
	stub := &stubProofFetcher{
		artifacts: []VerifiedArtifact{{LeafIndex: 0, Nonce: 1, VectorB64: ""}},
	}

	work := participantWork{
		address: "cosmos1test",
		modelId: "test-model",
		pubKey:  "testpubkey",
		count:   100,
		url:     "http://participant",
	}

	pocParams := &types.PocParams{
		PocStrongerRngEnabled: pocStrongerRng,
		Models: []*types.PoCModelConfig{
			{
				ModelId:           "test-model",
				SeqLen:            256,
				StatTest:          types.DefaultPoCStatTestParams(),
				WeightScaleFactor: types.DecimalFromFloat(1.0),
			},
		},
	}

	nodeCounter := 0
	result := v.validateParticipant(
		context.Background(),
		0, &work, stub,
		[]broker.NodeResponse{testNode}, &nodeCounter,
		1000, "sampling-hash", "start-hash",
		pocParams, 1,
		nil,
	)

	require.Equal(t, validateDispatched, result)

	mockClient.Mu.Lock()
	require.Equal(t, 1, mockClient.GenerateV2Called, "GenerateV2 should be called once")
	require.NotNil(t, mockClient.LastGenerateV2Req)
	req := *mockClient.LastGenerateV2Req
	mockClient.Mu.Unlock()
	return &req
}

// TestValidateParticipant_StrongerRngPropagated asserts that PocStrongerRng from
// PocParams reaches the GenerateV2 call sent to the ML node during validation.
// This is the validation (validator.go) path, analogous to
// TestStartPoCNodeCommandV2_StrongerRngPropagated for the broker (InitGenerateV2) path.
func TestValidateParticipant_StrongerRngPropagated(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		req := runValidateParticipant(t, true)
		assert.True(t, req.PocStrongerRng, "PocStrongerRng must be forwarded to GenerateV2 when enabled")
	})

	t.Run("disabled", func(t *testing.T) {
		req := runValidateParticipant(t, false)
		assert.False(t, req.PocStrongerRng, "PocStrongerRng must be forwarded to GenerateV2 when disabled")
	})
}

func TestWorkerReportsInvalidAfterRetryExhaustion(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("SubmitPocValidationsV2", mock.MatchedBy(func(msg *types.MsgSubmitPocValidationsV2) bool {
		if msg.PocStageStartBlockHeight != 1000 || len(msg.Validations) != 1 {
			return false
		}
		validation := msg.Validations[0]
		return validation.ParticipantAddress == "cosmos1unresponsive" &&
			validation.ModelId == "test-model" &&
			validation.ValidatedWeight == -1
	})).Return(nil).Once()

	v := &OffChainValidator{
		recorder: recorder,
		config: ValidationConfig{
			MaxRetries:   1,
			RetryBackoff: time.Millisecond,
		},
	}

	workChan := make(chan participantWork, 1)
	workChan <- participantWork{
		address: "cosmos1unresponsive",
		modelId: "test-model",
		count:   100,
		url:     "http://participant",
	}

	testNode := broker.NodeResponse{
		Node: broker.Node{
			Host:    "127.0.0.1",
			PoCPort: 8080,
			NodeNum: 1,
			Models: map[string]broker.ModelArgs{
				"test-model": {},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dispatchedCount := 0
	failCount := 0
	abstainCount := 0
	pendingCount := 1
	var statsMu sync.Mutex

	v.worker(
		ctx,
		cancel,
		0,
		workChan,
		&failingProofFetcher{err: errors.New("participant unavailable")},
		[]broker.NodeResponse{testNode},
		1000,
		"sampling-hash",
		"start-hash",
		&types.PocParams{},
		1,
		nil,
		&statsMu,
		&dispatchedCount,
		&failCount,
		&abstainCount,
		&pendingCount,
	)

	require.Equal(t, 0, dispatchedCount)
	require.Equal(t, 1, failCount)
	require.Equal(t, 0, abstainCount)
	require.Equal(t, 0, pendingCount)
	recorder.AssertExpectations(t)
}

// generateFailingNodeClient fails only the GenerateV2 dispatch, i.e. our own side.
type generateFailingNodeClient struct {
	*mlnodeclient.MockClient
	err error
}

func (c *generateFailingNodeClient) GenerateV2(context.Context, mlnodeclient.PoCGenerateRequestV2) (*mlnodeclient.PoCGenerateResponseV2, error) {
	return nil, c.err
}

func nodeForModel(modelID string) broker.NodeResponse {
	return broker.NodeResponse{
		Node: broker.Node{
			Host:    "127.0.0.1",
			PoCPort: 8080,
			NodeNum: 1,
			Models: map[string]broker.ModelArgs{
				modelID: {},
			},
		},
	}
}

func testModelPocParams() *types.PocParams {
	return &types.PocParams{
		Models: []*types.PoCModelConfig{
			{
				ModelId:           "test-model",
				SeqLen:            256,
				StatTest:          types.DefaultPoCStatTestParams(),
				WeightScaleFactor: types.DecimalFromFloat(1.0),
			},
		},
	}
}

func testWork() participantWork {
	return participantWork{
		address: "cosmos1validatee",
		modelId: "test-model",
		pubKey:  "testpubkey",
		count:   100,
		url:     "http://participant",
	}
}

// validArtifactFetcher returns one artifact with nonce=1, which against count=100
// gives porosity 0.01 - well below the threshold.
func validArtifactFetcher() *stubProofFetcher {
	return &stubProofFetcher{
		artifacts: []VerifiedArtifact{{LeafIndex: 0, Nonce: 1, VectorB64: ""}},
	}
}

// runAbstainWorker drives a single work item through worker and returns the stats.
func runAbstainWorker(
	t *testing.T,
	v *OffChainValidator,
	fetcher proofFetcher,
	nodes []broker.NodeResponse,
) (dispatchedCount, failCount, abstainCount, pendingCount int) {
	t.Helper()

	workChan := make(chan participantWork, 4)
	workChan <- testWork()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pendingCount = 1
	var statsMu sync.Mutex

	v.worker(
		ctx,
		cancel,
		0,
		workChan,
		fetcher,
		nodes,
		1000,
		"sampling-hash",
		"start-hash",
		testModelPocParams(),
		1,
		nil,
		&statsMu,
		&dispatchedCount,
		&failCount,
		&abstainCount,
		&pendingCount,
	)

	return dispatchedCount, failCount, abstainCount, pendingCount
}

// TestValidateParticipantAbstainsWithoutModelExecutor covers the case where proofs
// verify against the commit but this validator has no ML node for the model.
func TestValidateParticipantAbstainsWithoutModelExecutor(t *testing.T) {
	v := &OffChainValidator{
		nodeBroker:  &stubNodeBroker{client: mlnodeclient.NewMockClient()},
		callbackUrl: "http://callback",
	}

	work := testWork()
	nodeCounter := 0
	result := v.validateParticipant(
		context.Background(),
		0, &work, validArtifactFetcher(),
		[]broker.NodeResponse{nodeForModel("other-model")}, &nodeCounter,
		1000, "sampling-hash", "start-hash",
		testModelPocParams(), 1,
		nil,
	)

	require.Equal(t, validateAbstain, result)
	require.True(t, work.checksPassed, "validatee checks passed, so they must be marked done")
}

func TestWorkerAbstainsWithoutModelExecutor(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}

	v := &OffChainValidator{
		recorder:    recorder,
		nodeBroker:  &stubNodeBroker{client: mlnodeclient.NewMockClient()},
		callbackUrl: "http://callback",
		config: ValidationConfig{
			MaxRetries:   3,
			RetryBackoff: time.Millisecond,
		},
	}

	dispatchedCount, failCount, abstainCount, pendingCount := runAbstainWorker(
		t, v, validArtifactFetcher(), []broker.NodeResponse{nodeForModel("other-model")},
	)

	require.Equal(t, 0, dispatchedCount)
	require.Equal(t, 0, failCount)
	require.Equal(t, 1, abstainCount)
	require.Equal(t, 0, pendingCount)
	recorder.AssertNotCalled(t, "SubmitPocValidationsV2", mock.Anything)
}

// TestWorkerReportsInvalidWhenProofsFailWithoutModelExecutor is the counterpart:
// proof verification needs no ML node, so a missing executor must not suppress
// the fraud vote.
func TestWorkerReportsInvalidWhenProofsFailWithoutModelExecutor(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("SubmitPocValidationsV2", mock.MatchedBy(func(msg *types.MsgSubmitPocValidationsV2) bool {
		if msg.PocStageStartBlockHeight != 1000 || len(msg.Validations) != 1 {
			return false
		}
		validation := msg.Validations[0]
		return validation.ParticipantAddress == "cosmos1validatee" &&
			validation.ModelId == "test-model" &&
			validation.ValidatedWeight == -1
	})).Return(nil).Once()

	v := &OffChainValidator{
		recorder:    recorder,
		nodeBroker:  &stubNodeBroker{client: mlnodeclient.NewMockClient()},
		callbackUrl: "http://callback",
		config: ValidationConfig{
			MaxRetries:   3,
			RetryBackoff: time.Millisecond,
		},
	}

	dispatchedCount, failCount, abstainCount, pendingCount := runAbstainWorker(
		t, v,
		&failingProofFetcher{err: ErrProofVerificationFailed},
		[]broker.NodeResponse{nodeForModel("other-model")},
	)

	require.Equal(t, 0, dispatchedCount)
	require.Equal(t, 1, failCount)
	require.Equal(t, 0, abstainCount)
	require.Equal(t, 0, pendingCount)
	recorder.AssertExpectations(t)
}

func TestWorkerAbstainsWhenMLNodeRequestFails(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}

	v := &OffChainValidator{
		recorder: recorder,
		nodeBroker: &stubNodeBroker{client: &generateFailingNodeClient{
			MockClient: mlnodeclient.NewMockClient(),
			err:        errors.New("ml node unreachable"),
		}},
		callbackUrl: "http://callback",
		config: ValidationConfig{
			MaxRetries:   1,
			RetryBackoff: time.Millisecond,
		},
	}

	dispatchedCount, failCount, abstainCount, pendingCount := runAbstainWorker(
		t, v, validArtifactFetcher(), []broker.NodeResponse{nodeForModel("test-model")},
	)

	require.Equal(t, 0, dispatchedCount)
	require.Equal(t, 0, failCount)
	require.Equal(t, 1, abstainCount)
	require.Equal(t, 0, pendingCount)
	recorder.AssertNotCalled(t, "SubmitPocValidationsV2", mock.Anything)
}

// TestValidateParticipantSkipsValidateeChecksOncePassed pins the sticky invariant.
// The proof fetcher would fail permanently if the checks ran again, so abstaining
// proves they did not.
func TestValidateParticipantSkipsValidateeChecksOncePassed(t *testing.T) {
	v := &OffChainValidator{
		nodeBroker: &stubNodeBroker{client: &generateFailingNodeClient{
			MockClient: mlnodeclient.NewMockClient(),
			err:        errors.New("ml node unreachable"),
		}},
		callbackUrl: "http://callback",
	}

	work := testWork()
	work.checksPassed = true
	work.verified = []VerifiedArtifact{{LeafIndex: 0, Nonce: 1, VectorB64: ""}}

	nodeCounter := 0
	result := v.validateParticipant(
		context.Background(),
		0, &work, &failingProofFetcher{err: ErrProofVerificationFailed},
		[]broker.NodeResponse{nodeForModel("test-model")}, &nodeCounter,
		1000, "sampling-hash", "start-hash",
		testModelPocParams(), 1,
		nil,
	)

	require.Equal(t, validateAbstainRetry, result)
}

// TestWorkerAbstainsOnLocalRequestFailure covers a broken keyring: we never reach
// the validatee, so retry exhaustion must abstain rather than vote -1.
func TestWorkerAbstainsOnLocalRequestFailure(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}

	v := &OffChainValidator{
		recorder:    recorder,
		nodeBroker:  &stubNodeBroker{client: mlnodeclient.NewMockClient()},
		callbackUrl: "http://callback",
		config: ValidationConfig{
			MaxRetries:   1,
			RetryBackoff: time.Millisecond,
		},
	}

	fetcher := &failingProofFetcher{
		err: fmt.Errorf("%w: failed to sign request: keyring locked", ErrLocalRequestFailure),
	}
	dispatchedCount, failCount, abstainCount, pendingCount := runAbstainWorker(
		t, v, fetcher, []broker.NodeResponse{nodeForModel("test-model")},
	)

	require.Equal(t, 0, dispatchedCount)
	require.Equal(t, 0, failCount)
	require.Equal(t, 1, abstainCount)
	require.Equal(t, 0, pendingCount)
	recorder.AssertNotCalled(t, "SubmitPocValidationsV2", mock.Anything)
}

// TestWorkerAbstainsWhenWindowCloses covers a transient fetch error caused by the
// phase watcher cancelling mid-attempt: the validatee is not at fault, so the last
// attempt must abstain rather than vote -1.
func TestWorkerAbstainsWhenWindowCloses(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}

	v := &OffChainValidator{
		recorder:    recorder,
		nodeBroker:  &stubNodeBroker{client: mlnodeclient.NewMockClient()},
		callbackUrl: "http://callback",
		config: ValidationConfig{
			MaxRetries:   1, // no retries left, so the result settles immediately
			RetryBackoff: time.Millisecond,
		},
	}

	workChan := make(chan participantWork, 4)
	workChan <- testWork()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The window closes while the fetch is in flight.
	fetcher := &cancellingProofFetcher{cancel: cancel, err: context.Canceled}

	pendingCount := 1
	var dispatchedCount, failCount, abstainCount int
	var statsMu sync.Mutex

	v.worker(
		ctx, cancel, 0, workChan, fetcher,
		[]broker.NodeResponse{nodeForModel("test-model")},
		1000, "sampling-hash", "start-hash",
		testModelPocParams(), 1, nil,
		&statsMu, &dispatchedCount, &failCount, &abstainCount, &pendingCount,
	)

	require.Equal(t, 0, failCount, "a cancelled window must not count as a validatee failure")
	require.Equal(t, 1, abstainCount)
	require.Equal(t, 0, pendingCount, "the item must still settle so the run can finish")
	recorder.AssertNotCalled(t, "SubmitPocValidationsV2", mock.Anything)
}

// TestValidateeChecksRunOnceAcrossDispatchRetries asserts that dispatch retries do
// not re-fetch proofs from a validatee that already passed.
func TestValidateeChecksRunOnceAcrossDispatchRetries(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}

	v := &OffChainValidator{
		recorder: recorder,
		nodeBroker: &stubNodeBroker{client: &generateFailingNodeClient{
			MockClient: mlnodeclient.NewMockClient(),
			err:        errors.New("ml node unreachable"),
		}},
		callbackUrl: "http://callback",
		config: ValidationConfig{
			MaxRetries:   4,
			RetryBackoff: time.Millisecond,
		},
	}

	fetcher := validArtifactFetcher()
	_, failCount, abstainCount, pendingCount := runAbstainWorker(
		t, v, fetcher, []broker.NodeResponse{nodeForModel("test-model")},
	)

	require.Equal(t, 1, fetcher.calls, "proofs must be fetched once, not once per dispatch attempt")
	require.Equal(t, 0, failCount)
	require.Equal(t, 1, abstainCount)
	require.Equal(t, 0, pendingCount)
	recorder.AssertNotCalled(t, "SubmitPocValidationsV2", mock.Anything)
}

// TestWorkerAbstainsOnEarlyGuardLocalFailure drives the enforce-mode early-share
// guard through worker: main proofs verify, but the inclusion fetch fails locally
// (broken keyring). That must settle as abstain with no -1 vote.
func TestWorkerAbstainsOnEarlyGuardLocalFailure(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}

	finalRoot := []byte{0xaa}
	earlyRoot := []byte{0xee}
	art := VerifiedArtifact{LeafIndex: 0, Nonce: 1, VectorB64: "vec"}
	localErr := fmt.Errorf("%w: failed to sign request: keyring locked", ErrLocalRequestFailure)

	fetcher := &scriptedProofFetcher{
		byRoot: map[string]struct {
			arts []VerifiedArtifact
			err  error
		}{
			// Phase 1 main fetch uses the committed final root.
			string(finalRoot): {arts: []VerifiedArtifact{art}},
			// Early-guard inclusion fetch uses the early root and fails locally.
			string(earlyRoot): {err: localErr},
		},
		byNonce: map[string]struct {
			arts []VerifiedArtifact
			err  error
		}{},
	}

	guard := NewEarlyShareGuard(earlyshare.Config{
		Mode:                  earlyshare.ModeEnforce,
		RequireInclusionProof: true,
		InclusionSampleSize:   1,
	}, newFakeStore())
	require.NotNil(t, guard)

	work := testWork()
	work.rootHash = finalRoot

	gr := &guardRuntime{
		guard: guard,
		decisions: map[string]earlyDecision{
			earlyShareKey(work.address, work.modelId): {
				requireInclusion: true,
				earlyCount:       10,
				earlyRoot:        earlyRoot,
				finalCount:       work.count,
			},
		},
		stage:             1000,
		validatorPubKey:   "pub",
		samplingBlockHash: "sampling-hash",
	}

	v := &OffChainValidator{
		recorder:    recorder,
		nodeBroker:  &stubNodeBroker{client: mlnodeclient.NewMockClient()},
		callbackUrl: "http://callback",
		pubKey:      "pub",
		config: ValidationConfig{
			MaxRetries:   1,
			RetryBackoff: time.Millisecond,
		},
	}

	workChan := make(chan participantWork, 4)
	workChan <- work

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var dispatchedCount, failCount, abstainCount int
	pendingCount := 1
	var statsMu sync.Mutex

	v.worker(
		ctx, cancel, 0, workChan, fetcher,
		[]broker.NodeResponse{nodeForModel("test-model")},
		1000, "sampling-hash", "start-hash",
		testModelPocParams(), 1, gr,
		&statsMu, &dispatchedCount, &failCount, &abstainCount, &pendingCount,
	)

	require.Equal(t, 0, dispatchedCount, "local early-guard failure must not reach ML dispatch")
	require.Equal(t, 0, failCount, "local early-guard failure must not vote -1")
	require.Equal(t, 1, abstainCount)
	require.Equal(t, 0, pendingCount)
	require.GreaterOrEqual(t, fetcher.calls, 2, "main proof fetch and early inclusion fetch must both run")
	recorder.AssertNotCalled(t, "SubmitPocValidationsV2", mock.Anything)
}
