package poc

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/teeverify"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
)

func trackerInValidationWindow() *chainphase.ChainPhaseTracker {
	tracker := &chainphase.ChainPhaseTracker{}
	epochParams := inferencetypes.EpochParams{
		EpochLength:           1000,
		PocStageDuration:      100,
		PocExchangeDuration:   50,
		PocValidationDelay:    10,
		PocValidationDuration: 100,
	}
	pocStart := int64(100)
	tracker.Update(
		chainphase.BlockInfo{Height: pocStart + 170, Hash: "h"},
		&inferencetypes.Epoch{Index: 1, PocStartBlockHeight: pocStart},
		&epochParams,
		true,
		nil,
	)
	return tracker
}

func trackerInConfirmationValidation() *chainphase.ChainPhaseTracker {
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
			Phase:         inferencetypes.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION,
		},
	)
	return tracker
}

type fakePendingQuery struct {
	resp        *inferencetypes.QueryPendingTeeAttestationsForStageResponse
	responses   []*inferencetypes.QueryPendingTeeAttestationsForStageResponse
	calls       int32
	returnError error
}

func (f *fakePendingQuery) PendingTeeAttestationsForStage(
	_ context.Context,
	_ *inferencetypes.QueryPendingTeeAttestationsForStageRequest,
	_ ...grpc.CallOption,
) (*inferencetypes.QueryPendingTeeAttestationsForStageResponse, error) {
	callNum := atomic.AddInt32(&f.calls, 1)
	if f.returnError != nil {
		return nil, f.returnError
	}
	if len(f.responses) > 0 {
		idx := int(callNum - 1)
		if idx >= len(f.responses) {
			idx = len(f.responses) - 1
		}
		return f.responses[idx], nil
	}
	return f.resp, nil
}

func att(subject, node, provider string) *inferencetypes.TeeAttestation {
	return &inferencetypes.TeeAttestation{
		ParticipantAddress: subject,
		NodeId:             node,
		PublicKey:          []byte("32-bytes-public-key-padding----X"),
		Provider:           provider,
		EnvelopeVersion:    "v1",
		Measurement:        testMeasurement,
		AttestedAtHeight:   100,
		ExpiresAtHeight:    200,
		Quote:              []byte("quote"),
	}
}

func TestTeeValidationWorker_SubmitsOkVerdictsAndSkipsOwn(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.
		On("SubmitTeeValidations", mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeValidations) bool {
			if len(msg.Validations) != 2 {
				return false
			}
			for _, v := range msg.Validations {
				if v.Verdict != inferencetypes.TeeVerdict_TEE_VERDICT_OK {
					return false
				}
				if v.SubjectAddress == "self" {
					return false
				}
			}
			return true
		})).
		Return(nil).Once()

	fq := &fakePendingQuery{
		resp: &inferencetypes.QueryPendingTeeAttestationsForStageResponse{
			Attestations: []*inferencetypes.TeeAttestation{
				att("addr-a", "node-1", testProvider),
				att("self", "node-2", testProvider),
				att("addr-b", "node-3", testProvider),
			},
		},
	}

	w := &TeeValidationWorker{
		recorder:    mockRecorder,
		tracker:     trackerInValidationWindow(),
		verifiers:   testRegistry(),
		queryClient: fq,
		ownAddress:  "self",
	}
	w.tick()
	mockRecorder.AssertExpectations(t)
	assert.Equal(t, int32(1), atomic.LoadInt32(&fq.calls))
	assert.Equal(t, int64(100), w.lastTallyHeight)
}

func TestTeeValidationWorker_RejectsWhenVerifierFails(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.
		On("SubmitTeeValidations", mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeValidations) bool {
			return len(msg.Validations) == 1 &&
				msg.Validations[0].Verdict == inferencetypes.TeeVerdict_TEE_VERDICT_REJECT &&
				msg.Validations[0].Reason != ""
		})).
		Return(nil).Once()

	registry := teeverify.NewRegistry()
	registry.Register(&failingVerifier{})

	fq := &fakePendingQuery{
		resp: &inferencetypes.QueryPendingTeeAttestationsForStageResponse{
			Attestations: []*inferencetypes.TeeAttestation{att("addr-a", "node-1", "failing")},
		},
	}
	w := &TeeValidationWorker{
		recorder:    mockRecorder,
		tracker:     trackerInValidationWindow(),
		verifiers:   registry,
		queryClient: fq,
	}
	w.tick()
	mockRecorder.AssertExpectations(t)
}

func TestTeeValidationWorker_RejectReasonSanitized(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.
		On("SubmitTeeValidations", mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeValidations) bool {
			if len(msg.Validations) != 1 {
				return false
			}
			reason := msg.Validations[0].Reason
			return utf8.ValidString(reason) &&
				!strings.Contains(reason, "\n") &&
				!strings.Contains(reason, "\x00") &&
				len(reason) <= maxValidationReasonBytes &&
				utf8.RuneCountInString(reason) <= maxValidationReasonRunes
		})).
		Return(nil).Once()

	registry := teeverify.NewRegistry()
	registry.Register(reasonFailingVerifier{
		reason: "bad\n" + string([]byte{0xff, 0xfe, 0x00}) + strings.Repeat("🙂", 600),
	})

	fq := &fakePendingQuery{
		resp: &inferencetypes.QueryPendingTeeAttestationsForStageResponse{
			Attestations: []*inferencetypes.TeeAttestation{att("addr-a", "node-1", "reason-failing")},
		},
	}
	w := &TeeValidationWorker{
		recorder:    mockRecorder,
		tracker:     trackerInValidationWindow(),
		verifiers:   registry,
		queryClient: fq,
	}
	w.tick()
	mockRecorder.AssertExpectations(t)
}

func TestTeeValidationWorker_SkipsUnsupportedProvider(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	fq := &fakePendingQuery{
		resp: &inferencetypes.QueryPendingTeeAttestationsForStageResponse{
			Attestations: []*inferencetypes.TeeAttestation{att("addr-a", "node-1", "unknown-provider")},
		},
	}
	w := &TeeValidationWorker{
		recorder:    mockRecorder,
		tracker:     trackerInValidationWindow(),
		verifiers:   testRegistry(),
		queryClient: fq,
	}
	w.tick()
	mockRecorder.AssertNotCalled(t, "SubmitTeeValidations", mock.Anything)
}

func TestTeeValidationWorker_DoesNotSubmitOutsideValidationWindow(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	fq := &fakePendingQuery{
		resp: &inferencetypes.QueryPendingTeeAttestationsForStageResponse{
			Attestations: []*inferencetypes.TeeAttestation{att("addr-a", "node-1", testProvider)},
		},
	}
	w := &TeeValidationWorker{
		recorder:    mockRecorder,
		tracker:     newTeeTrackerAt(120),
		verifiers:   testRegistry(),
		queryClient: fq,
	}
	w.tick()
	mockRecorder.AssertNotCalled(t, "SubmitTeeValidations", mock.Anything)
	assert.Equal(t, int32(0), atomic.LoadInt32(&fq.calls))
}

func TestTeeValidationWorker_DoesNotSubmitDuringConfirmationPoC(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	fq := &fakePendingQuery{
		resp: &inferencetypes.QueryPendingTeeAttestationsForStageResponse{
			Attestations: []*inferencetypes.TeeAttestation{att("addr-a", "node-1", testProvider)},
		},
	}
	w := &TeeValidationWorker{
		recorder:    mockRecorder,
		tracker:     trackerInConfirmationValidation(),
		verifiers:   testRegistry(),
		queryClient: fq,
	}
	w.tick()
	mockRecorder.AssertNotCalled(t, "SubmitTeeValidations", mock.Anything)
	assert.Equal(t, int32(0), atomic.LoadInt32(&fq.calls))
}

func TestTeeValidationWorker_StageProgressPreventsResubmit(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.
		On("SubmitTeeValidations", mock.Anything).
		Return(nil).Once()

	fq := &fakePendingQuery{
		resp: &inferencetypes.QueryPendingTeeAttestationsForStageResponse{
			Attestations: []*inferencetypes.TeeAttestation{att("addr-a", "node-1", testProvider)},
		},
	}
	w := &TeeValidationWorker{
		recorder:    mockRecorder,
		tracker:     trackerInValidationWindow(),
		verifiers:   testRegistry(),
		queryClient: fq,
	}
	w.tick()
	w.tick()
	mockRecorder.AssertExpectations(t)
}

func TestTeeValidationWorker_ResumesAfterSubmitFailure(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("SubmitTeeValidations", mock.Anything).Return(errors.New("boom")).Once()
	mockRecorder.On("SubmitTeeValidations", mock.Anything).Return(nil).Once()

	fq := &fakePendingQuery{
		resp: &inferencetypes.QueryPendingTeeAttestationsForStageResponse{
			Attestations: []*inferencetypes.TeeAttestation{att("addr-a", "node-1", testProvider)},
		},
	}
	w := &TeeValidationWorker{
		recorder:    mockRecorder,
		tracker:     trackerInValidationWindow(),
		verifiers:   testRegistry(),
		queryClient: fq,
	}
	w.tick()
	w.tick()
	mockRecorder.AssertExpectations(t)
}

func TestTeeValidationWorker_DoesNotCompleteOnInitialEmptyPending(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.
		On("SubmitTeeValidations", mock.Anything).
		Return(nil).Once()

	fq := &fakePendingQuery{
		responses: []*inferencetypes.QueryPendingTeeAttestationsForStageResponse{
			{Attestations: nil},
			{Attestations: []*inferencetypes.TeeAttestation{att("addr-a", "node-1", testProvider)}},
		},
	}
	w := &TeeValidationWorker{
		recorder:    mockRecorder,
		tracker:     trackerInValidationWindow(),
		verifiers:   testRegistry(),
		queryClient: fq,
	}

	w.tick()
	mockRecorder.AssertNotCalled(t, "SubmitTeeValidations", mock.Anything)

	w.tick()
	mockRecorder.AssertExpectations(t)
	assert.Equal(t, int32(2), atomic.LoadInt32(&fq.calls))
}

func TestTeeValidationWorker_DoesNotCompleteOnInitialZeroVerdicts(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.
		On("SubmitTeeValidations", mock.Anything).
		Return(nil).Once()

	fq := &fakePendingQuery{
		responses: []*inferencetypes.QueryPendingTeeAttestationsForStageResponse{
			{Attestations: []*inferencetypes.TeeAttestation{att("addr-a", "node-1", "unknown-provider")}},
			{Attestations: []*inferencetypes.TeeAttestation{att("addr-a", "node-1", testProvider)}},
		},
	}
	w := &TeeValidationWorker{
		recorder:    mockRecorder,
		tracker:     trackerInValidationWindow(),
		verifiers:   testRegistry(),
		queryClient: fq,
	}

	w.tick()
	mockRecorder.AssertNotCalled(t, "SubmitTeeValidations", mock.Anything)

	w.tick()
	mockRecorder.AssertExpectations(t)
	assert.Equal(t, int32(2), atomic.LoadInt32(&fq.calls))
}

func TestNewTeeValidationWorker_ZeroIntervalDisablesWorker(t *testing.T) {
	w := NewTeeValidationWorker(
		&cosmosclient.MockCosmosMessageClient{},
		trackerInValidationWindow(),
		testRegistry(),
		nil,
		&fakePendingQuery{},
		"self",
		0,
	)
	status := w.Status()
	assert.False(t, status.Enabled)
	assert.Equal(t, "interval<=0", status.DisabledReason)
	w.Close()
}

func TestNewTeeValidationWorker_NilQueryClientDisablesWorker(t *testing.T) {
	w := NewTeeValidationWorker(
		&cosmosclient.MockCosmosMessageClient{},
		trackerInValidationWindow(),
		testRegistry(),
		nil,
		nil,
		"self",
		time.Second,
	)
	status := w.Status()
	assert.False(t, status.Enabled)
	assert.Equal(t, "no query client", status.DisabledReason)
	w.Close()
}

func TestNewTeeValidationWorker_NilRecorderDisablesWorker(t *testing.T) {
	w := NewTeeValidationWorker(
		nil,
		trackerInValidationWindow(),
		testRegistry(),
		nil,
		&fakePendingQuery{},
		"self",
		1,
	)
	status := w.Status()
	assert.False(t, status.Enabled)
	assert.Equal(t, "no recorder", status.DisabledReason)
	w.Close()
}

func TestNewTeeValidationWorker_NilTrackerDisablesWorker(t *testing.T) {
	w := NewTeeValidationWorker(
		&cosmosclient.MockCosmosMessageClient{},
		nil,
		testRegistry(),
		nil,
		&fakePendingQuery{},
		"self",
		1,
	)
	status := w.Status()
	assert.False(t, status.Enabled)
	assert.Equal(t, "no chain phase tracker", status.DisabledReason)
	w.Close()
}

func TestNewTeeValidationWorker_EmptyOwnAddressDisablesWorker(t *testing.T) {
	w := NewTeeValidationWorker(
		&cosmosclient.MockCosmosMessageClient{},
		trackerInValidationWindow(),
		testRegistry(),
		nil,
		&fakePendingQuery{},
		"",
		time.Second,
	)
	status := w.Status()
	assert.False(t, status.Enabled)
	assert.Equal(t, "empty own address", status.DisabledReason)
	w.Close()
}

func TestSanitizeValidationReason_StripsControlsAndCaps(t *testing.T) {
	raw := "prefix\n" + string([]byte{0xff, 0xfe, 0x00}) + strings.Repeat("🙂", 600)
	got := sanitizeValidationReason(raw)

	assert.True(t, utf8.ValidString(got))
	assert.NotContains(t, got, "\n")
	assert.NotContains(t, got, "\x00")
	assert.LessOrEqual(t, len(got), maxValidationReasonBytes)
	assert.LessOrEqual(t, utf8.RuneCountInString(got), maxValidationReasonRunes)
	assert.True(t, strings.HasPrefix(got, "prefix"))
}

func TestTeeValidationWorker_RejectsOnMeasurementMismatch(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.
		On("SubmitTeeValidations", mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeValidations) bool {
			if len(msg.Validations) != 1 {
				return false
			}
			v := msg.Validations[0]
			return v.Verdict == inferencetypes.TeeVerdict_TEE_VERDICT_REJECT &&
				strings.Contains(v.Reason, "measurement mismatch") &&
				v.MeasurementSeen != ""
		})).
		Return(nil).Once()

	registry := teeverify.NewRegistry()
	registry.Register(fixedMeasurementVerifier{provider: testProvider, measurement: "cafebabe"})

	subject := att("addr-a", "node-1", testProvider)
	subject.Measurement = testMeasurement
	fq := &fakePendingQuery{
		resp: &inferencetypes.QueryPendingTeeAttestationsForStageResponse{
			Attestations: []*inferencetypes.TeeAttestation{subject},
		},
	}
	w := &TeeValidationWorker{
		recorder:    mockRecorder,
		tracker:     trackerInValidationWindow(),
		verifiers:   registry,
		queryClient: fq,
	}
	w.tick()
	mockRecorder.AssertExpectations(t)
}

func TestTeeValidationWorker_RejectsOnNilReport(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.
		On("SubmitTeeValidations", mock.MatchedBy(func(msg *inferencetypes.MsgSubmitTeeValidations) bool {
			return len(msg.Validations) == 1 &&
				msg.Validations[0].Verdict == inferencetypes.TeeVerdict_TEE_VERDICT_REJECT &&
				strings.Contains(msg.Validations[0].Reason, "nil report")
		})).
		Return(nil).Once()

	registry := teeverify.NewRegistry()
	registry.Register(nilReportVerifier{})

	fq := &fakePendingQuery{
		resp: &inferencetypes.QueryPendingTeeAttestationsForStageResponse{
			Attestations: []*inferencetypes.TeeAttestation{att("addr-a", "node-1", "nil-report")},
		},
	}
	w := &TeeValidationWorker{
		recorder:    mockRecorder,
		tracker:     trackerInValidationWindow(),
		verifiers:   registry,
		queryClient: fq,
	}
	w.tick()
	mockRecorder.AssertExpectations(t)
}

type fixedMeasurementVerifier struct {
	provider    string
	measurement string
}

func (v fixedMeasurementVerifier) Provider() string { return v.provider }
func (v fixedMeasurementVerifier) Verify(_ context.Context, _ teeverify.VerifyRequest) (*teeverify.Report, error) {
	return &teeverify.Report{Measurement: v.measurement, TcbStatus: "test"}, nil
}

type nilReportVerifier struct{}

func (nilReportVerifier) Provider() string { return "nil-report" }
func (nilReportVerifier) Verify(_ context.Context, _ teeverify.VerifyRequest) (*teeverify.Report, error) {
	return nil, nil
}

type failingVerifier struct{}

func (failingVerifier) Provider() string { return "failing" }
func (failingVerifier) Verify(_ context.Context, _ teeverify.VerifyRequest) (*teeverify.Report, error) {
	return nil, errors.New("mock-verifier rejection")
}

type reasonFailingVerifier struct {
	reason string
}

func (v reasonFailingVerifier) Provider() string { return "reason-failing" }
func (v reasonFailingVerifier) Verify(_ context.Context, _ teeverify.VerifyRequest) (*teeverify.Report, error) {
	return nil, errors.New(v.reason)
}
