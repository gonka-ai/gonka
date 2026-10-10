package accounting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// The wiring is what carries the fact to a reader; a lookup nobody calls reports nothing.
func TestCapability_ReachesTheParticipantsEndpoint(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 9, "m1")
	require.NoError(t, tracker.RecordDiff("e1", 1, false))

	handler := NewHandler(tracker,
		func(context.Context) (uint64, error) { return 9, nil },
		func(participant, _ string) HostCapability {
			return HostCapability{ProtocolVersionUnsupported: participant == "p0"}
		})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/epochs/current/participants", nil))
	require.Equal(t, http.StatusOK, recorder.Code)

	var body struct {
		Participants []ParticipantRecord `json:"participants"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	blocked := map[string]bool{}
	for _, record := range body.Participants {
		blocked[record.Participant] = record.Capability != nil && record.Capability.ProtocolVersionUnsupported
	}
	require.True(t, blocked["p0"])
	require.False(t, blocked["p1"])
}

// The counters say the gateway stopped sending. They cannot say the host runs a build that does not
// speak the escrow's protocol version, which is the fact an operator needs to act on.
func TestCapability_NamesWhyAHostIsUnusable(t *testing.T) {
	records := []ParticipantRecord{{Participant: "gonka1blocked"}, {Participant: "gonka1healthy"}}

	attachCapabilities(records, func(participant, _ string) HostCapability {
		if participant == "gonka1blocked" {
			return HostCapability{ProtocolVersionUnsupported: true, VersionRefusals: 3, ContextLimit: 8192}
		}
		return HostCapability{}
	})

	require.NotNil(t, records[0].Capability)
	require.True(t, records[0].Capability.ProtocolVersionUnsupported)
	require.Equal(t, uint64(3), records[0].Capability.VersionRefusals, "the count says how often, the flag only that it happened")
	require.Equal(t, uint64(8192), records[0].Capability.ContextLimit)
	require.Nil(t, records[1].Capability, "a host with nothing wrong carries no capability block")
}

// Every participant would otherwise gain an all-false block, which is noise on a response this plan
// is also shrinking.
func TestCapability_OmitsTheBlockWhenNothingIsKnown(t *testing.T) {
	records := []ParticipantRecord{{Participant: "gonka1healthy"}}

	attachCapabilities(records, func(string, string) HostCapability { return HostCapability{} })

	require.Nil(t, records[0].Capability)
}

// The gateway passes nil when it has no perf tracker.
func TestCapability_NoLookupLeavesRecordsUntouched(t *testing.T) {
	records := []ParticipantRecord{{Participant: "gonka1healthy"}}

	attachCapabilities(records, nil)

	require.Nil(t, records[0].Capability)
}

// A lookup that reports a limit but no block still tells the reader something actionable.
func TestCapability_AContextLimitAloneIsWorthReporting(t *testing.T) {
	records := []ParticipantRecord{{Participant: "gonka1small"}}

	attachCapabilities(records, func(string, string) HostCapability { return HostCapability{ContextLimit: 4096} })

	require.NotNil(t, records[0].Capability)
	require.Equal(t, uint64(4096), records[0].Capability.ContextLimit)
}
