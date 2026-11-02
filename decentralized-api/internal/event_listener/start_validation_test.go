package event_listener

import (
	"sync/atomic"
	"testing"
	"time"

	"decentralized-api/chainphase"

	"github.com/stretchr/testify/require"
)

type recordingValidator struct {
	prepared          bool
	prepareCalls      atomic.Int32
	validateAllCalls  atomic.Int32
	openChallengeRuns atomic.Int32
}

func (r *recordingValidator) PrepareValidationNodes() bool {
	r.prepareCalls.Add(1)
	return r.prepared
}

func (r *recordingValidator) ValidateAll(int64, string) { r.validateAllCalls.Add(1) }

func (r *recordingValidator) ValidateOpenChallenges() { r.openChallengeRuns.Add(1) }

func (r *recordingValidator) MaybeCaptureEarlyShare(chainphase.EpochState) {}

func (r *recordingValidator) SyncArtifactStoreStage(chainphase.EpochState) {}

func TestStartValidationRunsBothPassesAfterNodesStop(t *testing.T) {
	validator := &recordingValidator{prepared: true}
	d := &OnNewBlockDispatcher{offChainValidator: validator}

	d.startValidation(func() { validator.ValidateAll(100, "hash") })

	require.Eventually(t, func() bool {
		return validator.validateAllCalls.Load() == 1 && validator.openChallengeRuns.Load() == 1
	}, time.Second, 5*time.Millisecond)
	require.Equal(t, int32(1), validator.prepareCalls.Load())
}

func TestStartValidationSkipsWhenNodesWereNotStopped(t *testing.T) {
	validator := &recordingValidator{prepared: false}
	d := &OnNewBlockDispatcher{offChainValidator: validator}

	d.startValidation(func() { validator.ValidateAll(100, "hash") })

	require.Never(t, func() bool {
		return validator.validateAllCalls.Load() > 0 || validator.openChallengeRuns.Load() > 0
	}, 100*time.Millisecond, 5*time.Millisecond)
	require.Equal(t, int32(1), validator.prepareCalls.Load())
}
