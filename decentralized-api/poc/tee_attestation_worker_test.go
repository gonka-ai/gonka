package poc

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/mlnodeclient"
	"decentralized-api/teeverify"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

const (
	testProvider    = "test-tdx"
	testMeasurement = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
)

type acceptingTestVerifier struct{}

func (acceptingTestVerifier) Provider() string { return testProvider }
func (acceptingTestVerifier) Verify(_ context.Context, req teeverify.VerifyRequest) (*teeverify.Report, error) {
	if len(req.PublicKey) == 0 {
		return nil, errors.New("acceptingTestVerifier: empty public key")
	}
	digest := sha256.Sum256(req.PublicKey)
	return &teeverify.Report{
		Measurement: testMeasurement,
		TcbStatus:   "test",
		QuoteHash:   digest[:],
	}, nil
}

func testRegistry() *teeverify.Registry {
	r := teeverify.NewRegistry()
	r.Register(acceptingTestVerifier{})
	return r
}

type stubBroker struct {
	nodes        []broker.NodeResponse
	mockClient   mlnodeclient.MLNodeClient
	errFromNodes error
}

func (s *stubBroker) GetNodes() ([]broker.NodeResponse, error) {
	if s.errFromNodes != nil {
		return nil, s.errFromNodes
	}
	return s.nodes, nil
}
func (s *stubBroker) NewNodeClient(_ *broker.Node) mlnodeclient.MLNodeClient {
	return s.mockClient
}

func makeBrokerNode(id string) broker.NodeResponse {
	return broker.NodeResponse{Node: broker.Node{Id: id, Host: "127.0.0.1", PoCPort: 8080}}
}

func newTeeTrackerAt(blockHeight int64) *chainphase.ChainPhaseTracker {
	tracker := &chainphase.ChainPhaseTracker{}
	epochParams := inferencetypes.EpochParams{
		EpochLength:           1000,
		PocStageDuration:      100,
		PocExchangeDuration:   50,
		PocValidationDelay:    10,
		PocValidationDuration: 100,
	}
	tracker.Update(
		chainphase.BlockInfo{Height: blockHeight, Hash: "h"},
		&inferencetypes.Epoch{Index: 1, PocStartBlockHeight: 100},
		&epochParams,
		true,
		nil,
	)
	return tracker
}

func newTeeTrackerInConfirmationGeneration() *chainphase.ChainPhaseTracker {
	tracker := &chainphase.ChainPhaseTracker{}
	epochParams := inferencetypes.EpochParams{
		EpochLength:           1000,
		PocStageDuration:      100,
		PocExchangeDuration:   50,
		PocValidationDelay:    10,
		PocValidationDuration: 100,
	}
	tracker.Update(
		chainphase.BlockInfo{Height: 500, Hash: "h"},
		&inferencetypes.Epoch{Index: 1, PocStartBlockHeight: 100},
		&epochParams,
		true,
		&inferencetypes.ConfirmationPoCEvent{
			TriggerHeight: 450,
			Phase:         inferencetypes.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
		},
	)
	return tracker
}

func teeIdentity() *mlnodeclient.EncryptedIdentity {
	return &mlnodeclient.EncryptedIdentity{
		Provider:        testProvider,
		EnvelopeVersion: "v1",
		PublicKey:       []byte("32-bytes-public-key-padding----X"),
		Quote:           []byte("raw-quote-bytes"),
	}
}

func TestTeeAttestationWorker_SubmitsOnePerStage(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("SubmitTeeAttestation", mock.AnythingOfType("*types.MsgSubmitTeeAttestation")).
		Return(nil).Once()

	mockClient := mlnodeclient.NewMockClient()
	mockClient.EncryptedIdentity = teeIdentity()

	stub := &stubBroker{
		nodes:      []broker.NodeResponse{makeBrokerNode("node-1"), makeBrokerNode("node-2")},
		mockClient: mockClient,
	}

	worker := &TeeAttestationWorker{
		recorder:   mockRecorder,
		nodeBroker: stub,
		tracker:    newTeeTrackerAt(120),
	}

	worker.tick()
	worker.tick()
	mockRecorder.AssertExpectations(t)
	assert.Equal(t, int64(100), worker.lastSubmittedHeight)
	assert.Equal(t, 2, mockClient.GetEncryptedIdentityCalled)
}

func TestTeeAttestationWorker_SubmitsRawQuoteWithHash(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("SubmitTeeAttestation",
		mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeAttestation) bool {
			if len(msg.Attestations) != 1 {
				return false
			}
			e := msg.Attestations[0]
			return len(e.Quote) > 0 && len(e.QuoteHash) == 32
		})).Return(nil).Once()

	mockClient := mlnodeclient.NewMockClient()
	mockClient.EncryptedIdentity = teeIdentity()

	worker := &TeeAttestationWorker{
		recorder: mockRecorder,
		nodeBroker: &stubBroker{
			nodes:      []broker.NodeResponse{makeBrokerNode("node-1")},
			mockClient: mockClient,
		},
		tracker: newTeeTrackerAt(120),
	}
	worker.tick()
	mockRecorder.AssertExpectations(t)
}

func TestTeeAttestationWorker_SkipsNodesWithoutIdentity(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.
		On("SubmitTeeAttestation", mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeAttestation) bool {
			return len(msg.Attestations) == 1 && msg.Attestations[0].NodeId == "node-yes"
		})).
		Return(nil).Once()

	yesClient := mlnodeclient.NewMockClient()
	yesClient.EncryptedIdentity = teeIdentity()
	noClient := mlnodeclient.NewMockClient()
	noClient.EncryptedIdentityError = mlnodeclient.ErrEncryptedIdentityDisabled

	clients := map[string]mlnodeclient.MLNodeClient{
		"node-yes": yesClient,
		"node-no":  noClient,
	}
	worker := &TeeAttestationWorker{
		recorder: mockRecorder,
		nodeBroker: &dispatchingBroker{
			nodes:   []broker.NodeResponse{makeBrokerNode("node-yes"), makeBrokerNode("node-no")},
			clients: clients,
		},
		tracker: newTeeTrackerAt(120),
	}
	worker.tick()
	mockRecorder.AssertExpectations(t)
}

func TestTeeAttestationWorker_SkipsNodesWithoutQuote(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockClient := mlnodeclient.NewMockClient()
	mockClient.EncryptedIdentity = &mlnodeclient.EncryptedIdentity{
		Provider:        "intel-tdx-lite",
		EnvelopeVersion: "v1",
		PublicKey:       []byte("32-bytes-public-key-padding----X"),
		Quote:           nil,
	}
	worker := &TeeAttestationWorker{
		recorder: mockRecorder,
		nodeBroker: &stubBroker{
			nodes:      []broker.NodeResponse{makeBrokerNode("node-1")},
			mockClient: mockClient,
		},
		tracker: newTeeTrackerAt(120),
	}
	worker.tick()
	mockRecorder.AssertNotCalled(t, "SubmitTeeAttestation", mock.Anything)
	assert.Equal(t, int64(0), worker.lastSubmittedHeight)
}

func TestTeeAttestationWorker_RetriesAfterFailure(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("SubmitTeeAttestation", mock.Anything).Return(errors.New("broadcast failed")).Once()
	mockRecorder.On("SubmitTeeAttestation", mock.Anything).Return(nil).Once()

	mockClient := mlnodeclient.NewMockClient()
	mockClient.EncryptedIdentity = teeIdentity()
	worker := &TeeAttestationWorker{
		recorder: mockRecorder,
		nodeBroker: &stubBroker{
			nodes:      []broker.NodeResponse{makeBrokerNode("node-1")},
			mockClient: mockClient,
		},
		tracker: newTeeTrackerAt(120),
	}
	worker.tick()
	assert.Equal(t, int64(0), worker.lastSubmittedHeight)
	worker.tick()
	assert.Equal(t, int64(100), worker.lastSubmittedHeight)
	mockRecorder.AssertExpectations(t)
}

func TestTeeAttestationWorker_DoesNothingOutsideWindow(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockClient := mlnodeclient.NewMockClient()
	mockClient.EncryptedIdentity = teeIdentity()
	worker := &TeeAttestationWorker{
		recorder: mockRecorder,
		nodeBroker: &stubBroker{
			nodes:      []broker.NodeResponse{makeBrokerNode("node-1")},
			mockClient: mockClient,
		},
		tracker: newTeeTrackerAt(300),
	}
	worker.tick()
	mockRecorder.AssertNotCalled(t, "SubmitTeeAttestation", mock.Anything)
	assert.Equal(t, 0, mockClient.GetEncryptedIdentityCalled)
}

func TestTeeAttestationWorker_DoesNothingDuringConfirmationPoC(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockClient := mlnodeclient.NewMockClient()
	mockClient.EncryptedIdentity = teeIdentity()
	worker := &TeeAttestationWorker{
		recorder: mockRecorder,
		nodeBroker: &stubBroker{
			nodes:      []broker.NodeResponse{makeBrokerNode("node-1")},
			mockClient: mockClient,
		},
		tracker: newTeeTrackerInConfirmationGeneration(),
	}
	worker.tick()
	mockRecorder.AssertNotCalled(t, "SubmitTeeAttestation", mock.Anything)
	assert.Equal(t, 0, mockClient.GetEncryptedIdentityCalled)
}

type stubChainParams struct {
	maxEntries uint32
	err        error
	calls      int
}

func (s *stubChainParams) GetTeeAttestationParams(_ context.Context) (*inferencetypes.TeeAttestationParams, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &inferencetypes.TeeAttestationParams{
		MaxEntriesPerMessage: s.maxEntries,
	}, nil
}

func TestTeeAttestationWorker_ChunksByMaxEntriesPerMessage(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("SubmitTeeAttestation",
		mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeAttestation) bool {
			return len(msg.Attestations) == 2
		})).Return(nil).Twice()
	mockRecorder.On("SubmitTeeAttestation",
		mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeAttestation) bool {
			return len(msg.Attestations) == 1
		})).Return(nil).Once()

	mockClient := mlnodeclient.NewMockClient()
	mockClient.EncryptedIdentity = teeIdentity()

	worker := &TeeAttestationWorker{
		recorder: mockRecorder,
		nodeBroker: &stubBroker{
			nodes: []broker.NodeResponse{
				makeBrokerNode("node-1"),
				makeBrokerNode("node-2"),
				makeBrokerNode("node-3"),
				makeBrokerNode("node-4"),
				makeBrokerNode("node-5"),
			},
			mockClient: mockClient,
		},
		tracker:     newTeeTrackerAt(120),
		chainParams: &stubChainParams{maxEntries: 2},
	}

	worker.tick()
	mockRecorder.AssertExpectations(t)
	assert.Equal(t, int64(100), worker.lastSubmittedHeight)
	assert.Equal(t, 5, worker.lastSubmittedCount)
}

func TestTeeAttestationWorker_ResumesAfterChunkFailure(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.
		On("SubmitTeeAttestation",
			mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeAttestation) bool {
				return len(msg.Attestations) == 2 &&
					msg.Attestations[0].NodeId == "node-1" &&
					msg.Attestations[1].NodeId == "node-2"
			})).Return(nil).Once()
	mockRecorder.
		On("SubmitTeeAttestation",
			mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeAttestation) bool {
				return len(msg.Attestations) == 1 && msg.Attestations[0].NodeId == "node-3"
			})).Return(errors.New("broadcast failed")).Once()
	mockRecorder.
		On("SubmitTeeAttestation",
			mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeAttestation) bool {
				return len(msg.Attestations) == 1 && msg.Attestations[0].NodeId == "node-3"
			})).Return(nil).Once()

	mockClient := mlnodeclient.NewMockClient()
	mockClient.EncryptedIdentity = teeIdentity()

	worker := &TeeAttestationWorker{
		recorder: mockRecorder,
		nodeBroker: &stubBroker{
			nodes: []broker.NodeResponse{
				makeBrokerNode("node-2"),
				makeBrokerNode("node-3"),
				makeBrokerNode("node-1"),
			},
			mockClient: mockClient,
		},
		tracker:     newTeeTrackerAt(120),
		chainParams: &stubChainParams{maxEntries: 2},
	}

	worker.tick()
	assert.False(t, worker.stageCompleted)
	assert.Equal(t, int64(100), worker.lastSubmittedHeight)
	assert.Equal(t, 2, worker.lastSubmittedCount)

	worker.tick()
	assert.True(t, worker.stageCompleted)
	assert.Equal(t, int64(100), worker.lastSubmittedHeight)
	assert.Equal(t, 3, worker.lastSubmittedCount)
	mockRecorder.AssertExpectations(t)
}

func TestTeeAttestationWorker_FallsBackToSingleEntriesAfterChunkFailure(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.
		On("SubmitTeeAttestation",
			mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeAttestation) bool {
				return len(msg.Attestations) == 2 &&
					msg.Attestations[0].NodeId == "node-1" &&
					msg.Attestations[1].NodeId == "node-2"
			})).Return(errors.New("batch rejected")).Once()
	mockRecorder.
		On("SubmitTeeAttestation",
			mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeAttestation) bool {
				return len(msg.Attestations) == 1 && msg.Attestations[0].NodeId == "node-1"
			})).Return(nil).Once()
	mockRecorder.
		On("SubmitTeeAttestation",
			mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeAttestation) bool {
				return len(msg.Attestations) == 1 && msg.Attestations[0].NodeId == "node-2"
			})).Return(nil).Once()
	mockRecorder.
		On("SubmitTeeAttestation",
			mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeAttestation) bool {
				return len(msg.Attestations) == 1 && msg.Attestations[0].NodeId == "node-3"
			})).Return(nil).Once()

	mockClient := mlnodeclient.NewMockClient()
	mockClient.EncryptedIdentity = teeIdentity()

	worker := &TeeAttestationWorker{
		recorder: mockRecorder,
		nodeBroker: &stubBroker{
			nodes: []broker.NodeResponse{
				makeBrokerNode("node-2"),
				makeBrokerNode("node-1"),
				makeBrokerNode("node-3"),
			},
			mockClient: mockClient,
		},
		tracker:     newTeeTrackerAt(120),
		chainParams: &stubChainParams{maxEntries: 2},
	}

	worker.tick()
	assert.True(t, worker.stageCompleted)
	assert.Equal(t, int64(100), worker.lastSubmittedHeight)
	assert.Equal(t, 3, worker.lastSubmittedCount)
	mockRecorder.AssertExpectations(t)
}

func TestTeeAttestationWorker_DoesNotCompleteStageOnTransientIdentityFailures(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.
		On("SubmitTeeAttestation",
			mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeAttestation) bool {
				return len(msg.Attestations) == 1 && msg.Attestations[0].NodeId == "node-ok"
			})).Return(nil).Once()
	mockRecorder.
		On("SubmitTeeAttestation",
			mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeAttestation) bool {
				return len(msg.Attestations) == 1 && msg.Attestations[0].NodeId == "node-flaky"
			})).Return(nil).Once()

	okClient := mlnodeclient.NewMockClient()
	okClient.EncryptedIdentity = teeIdentity()
	flakyClient := mlnodeclient.NewMockClient()
	flakyClient.EncryptedIdentityError = errors.New("temporary timeout")

	worker := &TeeAttestationWorker{
		recorder: mockRecorder,
		nodeBroker: &dispatchingBroker{
			nodes: []broker.NodeResponse{
				makeBrokerNode("node-ok"),
				makeBrokerNode("node-flaky"),
			},
			clients: map[string]mlnodeclient.MLNodeClient{
				"node-ok":    okClient,
				"node-flaky": flakyClient,
			},
		},
		tracker:     newTeeTrackerAt(120),
		chainParams: &stubChainParams{maxEntries: 16},
	}

	worker.tick()
	assert.False(t, worker.stageCompleted)
	assert.Equal(t, int64(100), worker.lastSubmittedHeight)
	assert.Equal(t, 1, worker.lastSubmittedCount)

	flakyClient.EncryptedIdentityError = nil
	flakyClient.EncryptedIdentity = teeIdentity()

	worker.tick()
	assert.True(t, worker.stageCompleted)
	assert.Equal(t, int64(100), worker.lastSubmittedHeight)
	assert.Equal(t, 2, worker.lastSubmittedCount)
	mockRecorder.AssertExpectations(t)
}

func TestTeeAttestationWorker_ResetsProgressAcrossStages(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("SubmitTeeAttestation", mock.Anything).Return(nil).Twice()

	mockClient := mlnodeclient.NewMockClient()
	mockClient.EncryptedIdentity = teeIdentity()

	tracker := newTeeTrackerAt(120)
	worker := &TeeAttestationWorker{
		recorder: mockRecorder,
		nodeBroker: &stubBroker{
			nodes:      []broker.NodeResponse{makeBrokerNode("node-1")},
			mockClient: mockClient,
		},
		tracker:     tracker,
		chainParams: &stubChainParams{maxEntries: 16},
	}
	worker.tick()
	assert.Equal(t, int64(100), worker.lastSubmittedHeight)

	epochParams := inferencetypes.EpochParams{
		EpochLength:           1000,
		PocStageDuration:      100,
		PocExchangeDuration:   50,
		PocValidationDelay:    10,
		PocValidationDuration: 100,
	}
	tracker.Update(
		chainphase.BlockInfo{Height: 1120, Hash: "h2"},
		&inferencetypes.Epoch{Index: 2, PocStartBlockHeight: 1100},
		&epochParams,
		true,
		nil,
	)
	worker.tick()
	assert.Equal(t, int64(1100), worker.lastSubmittedHeight)
	mockRecorder.AssertExpectations(t)
}

func TestChunkAttestations(t *testing.T) {
	mk := func(n int) []*inferencetypes.TeeAttestationEntry {
		out := make([]*inferencetypes.TeeAttestationEntry, n)
		for i := 0; i < n; i++ {
			out[i] = &inferencetypes.TeeAttestationEntry{}
		}
		return out
	}
	assert.Len(t, chunkEntries(mk(0), 5), 1)
	assert.Len(t, chunkEntries(mk(3), 5), 1)
	assert.Len(t, chunkEntries(mk(5), 5), 1)
	assert.Len(t, chunkEntries(mk(6), 5), 2)
	assert.Len(t, chunkEntries(mk(11), 5), 3)
	assert.Len(t, chunkEntries(mk(10), 0), 1)
}

func TestResolveMaxEntries_FallsBackToDefaultWhenChainErrors(t *testing.T) {
	w := &TeeAttestationWorker{
		chainParams: &stubChainParams{err: errors.New("chain down")},
	}
	got := w.resolveMaxEntries()
	assert.Equal(t, int(inferencetypes.DefaultMaxEntriesPerTeeAttestationMessage), got)
}

func TestResolveMaxEntries_FallsBackToDefaultWhenChainReturnsZero(t *testing.T) {
	w := &TeeAttestationWorker{
		chainParams: &stubChainParams{maxEntries: 0},
	}
	got := w.resolveMaxEntries()
	assert.Equal(t, int(inferencetypes.DefaultMaxEntriesPerTeeAttestationMessage), got)
}

func TestResolveMaxEntries_QueriedEveryTick(t *testing.T) {
	stub := &stubChainParams{maxEntries: 17}
	w := &TeeAttestationWorker{chainParams: stub}
	first := w.resolveMaxEntries()
	second := w.resolveMaxEntries()
	assert.Equal(t, 17, first)
	assert.Equal(t, 17, second)
	assert.Equal(t, 2, stub.calls)
}

func TestNewTeeAttestationWorker_ZeroIntervalDisablesWorker(t *testing.T) {
	w := NewTeeAttestationWorker(
		&cosmosclient.MockCosmosMessageClient{},
		&stubBroker{},
		newTeeTrackerAt(120),
		nil,
		0,
	)
	assert.NotNil(t, w)
	status := w.Status()
	assert.False(t, status.Enabled)
	assert.Equal(t, "interval<=0", status.DisabledReason)
	w.Close()
}

func TestNewTeeAttestationWorker_NilRecorderDisablesWorker(t *testing.T) {
	w := NewTeeAttestationWorker(
		nil,
		&stubBroker{},
		newTeeTrackerAt(120),
		nil,
		1,
	)
	status := w.Status()
	assert.False(t, status.Enabled)
	assert.Equal(t, "no recorder", status.DisabledReason)
	w.Close()
}

func TestNewTeeAttestationWorker_NilNodeBrokerDisablesWorker(t *testing.T) {
	w := NewTeeAttestationWorker(
		&cosmosclient.MockCosmosMessageClient{},
		nil,
		newTeeTrackerAt(120),
		nil,
		1,
	)
	status := w.Status()
	assert.False(t, status.Enabled)
	assert.Equal(t, "no node broker", status.DisabledReason)
	w.Close()
}

func TestNewTeeAttestationWorker_NilTrackerDisablesWorker(t *testing.T) {
	w := NewTeeAttestationWorker(
		&cosmosclient.MockCosmosMessageClient{},
		&stubBroker{},
		nil,
		nil,
		1,
	)
	status := w.Status()
	assert.False(t, status.Enabled)
	assert.Equal(t, "no chain phase tracker", status.DisabledReason)
	w.Close()
}

type dispatchingBroker struct {
	nodes   []broker.NodeResponse
	clients map[string]mlnodeclient.MLNodeClient
}

func (d *dispatchingBroker) GetNodes() ([]broker.NodeResponse, error) { return d.nodes, nil }
func (d *dispatchingBroker) NewNodeClient(node *broker.Node) mlnodeclient.MLNodeClient {
	if c, ok := d.clients[node.Id]; ok {
		return c
	}
	return mlnodeclient.NewMockClient()
}
