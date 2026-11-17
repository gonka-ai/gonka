package bls

import (
	"bytes"
	"encoding/base64"
	"strconv"
	"testing"

	"decentralized-api/internal/event_listener/chainevents"

	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/stretchr/testify/require"
)

func reissueSigningEvent(requestID []byte, epoch uint64, hash []byte) *chainevents.JSONRPCResponse {
	const prefix = "inference.bls.EventThresholdSigningRequested."
	return &chainevents.JSONRPCResponse{Result: chainevents.Result{Events: map[string][]string{
		prefix + "request_id":            {strconv.Quote(base64.StdEncoding.EncodeToString(requestID))},
		prefix + "current_epoch_id":      {strconv.Quote(strconv.FormatUint(epoch, 10))},
		prefix + "message_hash":          {strconv.Quote(base64.StdEncoding.EncodeToString(hash))},
		prefix + "deadline_block_height": {"1000"},
	}}}
}

// The concrete Cosmos client has no injectable tx manager in this package.
// Deliberately incomplete shares let these tests observe the real event and
// epoch-selection path at its pre-broadcast validation boundary, without an
// external node or changing the production client interface for testing.
func TestThresholdSigningReissueSameRequestUsesNewEpoch(t *testing.T) {
	manager := NewBlsManager(createMockCosmosClient())
	requestID := bytes.Repeat([]byte{0x42}, 32)
	manager.cache.Store(&VerificationResult{EpochID: 700, DkgPhase: blstypes.DKGPhase_DKG_PHASE_SIGNED, IsParticipant: false})
	require.NoError(t, manager.ProcessThresholdSigningRequested(reissueSigningEvent(requestID, 700, bytes.Repeat([]byte{1}, 32))))

	manager.cache.Store(&VerificationResult{EpochID: 702, DkgPhase: blstypes.DKGPhase_DKG_PHASE_SIGNED, IsParticipant: true})
	require.Nil(t, manager.cache.Get(700), "normal epoch advancement evicts the old result")
	err := manager.ProcessThresholdSigningRequested(reissueSigningEvent(requestID, 702, bytes.Repeat([]byte{2}, 32)))
	require.ErrorContains(t, err, "no aggregated shares available", "the same request must reach signing with its new epoch's result")
}

func TestThresholdSigningReissueProcessesAllTransactionEventsAfterFailure(t *testing.T) {
	manager := NewBlsManager(createMockCosmosClient())
	// Cache in descending order so all three fixtures remain available.
	manager.cache.Store(&VerificationResult{EpochID: 703, DkgPhase: blstypes.DKGPhase_DKG_PHASE_SIGNED, IsParticipant: true})
	manager.cache.Store(&VerificationResult{EpochID: 702, DkgPhase: blstypes.DKGPhase_DKG_PHASE_SIGNED, IsParticipant: false})
	manager.cache.Store(&VerificationResult{EpochID: 701, DkgPhase: blstypes.DKGPhase_DKG_PHASE_SIGNED, IsParticipant: true})
	event := reissueSigningEvent(bytes.Repeat([]byte{1}, 32), 701, []byte{1})
	for _, next := range []*chainevents.JSONRPCResponse{
		reissueSigningEvent(bytes.Repeat([]byte{2}, 32), 702, bytes.Repeat([]byte{2}, 32)),
		reissueSigningEvent(bytes.Repeat([]byte{3}, 32), 703, bytes.Repeat([]byte{3}, 32)),
	} {
		for key, values := range next.Result.Events {
			event.Result.Events[key] = append(event.Result.Events[key], values...)
		}
	}
	before := make(map[string][]string, len(event.Result.Events))
	for key, values := range event.Result.Events {
		before[key] = append([]string(nil), values...)
	}
	err := manager.ProcessThresholdSigningRequested(event)
	require.ErrorContains(t, err, "threshold signing event 0: invalid message hash length")
	require.ErrorContains(t, err, "threshold signing event 2: failed to submit partial signatures")
	require.ErrorContains(t, err, "no aggregated shares available", "later events must run even when an earlier one fails")
	require.NotContains(t, err.Error(), "threshold signing event 1:", "the middle event must use its own nonparticipant epoch")
	require.Equal(t, before, event.Result.Events, "batch dispatch must not mutate shared event attributes")
}

func TestThresholdSigningReissueRejectsMismatchedEventAttributes(t *testing.T) {
	const prefix = "inference.bls.EventThresholdSigningRequested."
	for _, field := range []string{"request_id", "current_epoch_id", "message_hash", "deadline_block_height"} {
		for _, missing := range []bool{false, true} {
			t.Run(field+"/missing="+strconv.FormatBool(missing), func(t *testing.T) {
				manager := NewBlsManager(createMockCosmosClient())
				// No cached epoch or client: any attempt to process before
				// validating the complete array shape would panic on recovery.
				event := reissueSigningEvent(bytes.Repeat([]byte{1}, 32), 702, bytes.Repeat([]byte{2}, 32))
				key := prefix + field
				if missing {
					delete(event.Result.Events, key)
				} else {
					event.Result.Events[key] = append(event.Result.Events[key], event.Result.Events[key][0])
				}
				require.Error(t, manager.ProcessThresholdSigningRequested(event))
			})
		}
	}
	manager := NewBlsManager(createMockCosmosClient())
	require.Error(t, manager.ProcessThresholdSigningRequested(nil))
}

func TestThresholdSigningReissueProcessesNonparticipantBatch(t *testing.T) {
	manager := NewBlsManager(createMockCosmosClient())
	manager.cache.Store(&VerificationResult{EpochID: 702, DkgPhase: blstypes.DKGPhase_DKG_PHASE_SIGNED, IsParticipant: false})
	event := reissueSigningEvent(bytes.Repeat([]byte{1}, 32), 702, bytes.Repeat([]byte{2}, 32))
	for key, values := range event.Result.Events {
		event.Result.Events[key] = append(values, values[0])
	}
	require.NoError(t, manager.ProcessThresholdSigningRequested(event), "a batch with no local signing responsibility is successful")
}
