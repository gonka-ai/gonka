package testutil

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Decode one participant row carrying a gateway counter, keyed flat beside the count.
//  2. Assert the counter reads its escrow, slot, disposition and count.
//  3. Assert it counts as a ghost burn of its reason and of no other.
func TestAccountingCounter_ReadsTheGatewayCounterShape(t *testing.T) {
	const flat = `{"participants":[{"counters":[{"escrow_id":"1","slot_id":2,"disposition":"ghost","ghost_reason":"participant_cut_off_no_send","count":3}]}]}`

	var decoded AccountingParticipantsResponse
	require.NoError(t, json.Unmarshal([]byte(flat), &decoded))
	counter := decoded.Participants[0].Counters[0]
	require.Equal(t, "1", counter.EscrowID)
	require.Equal(t, uint32(2), counter.SlotID)
	require.Equal(t, "ghost", counter.Disposition)
	require.Equal(t, uint64(3), counter.Count)
	require.Equal(t, uint64(3), AccountingGhostCounterCount(decoded, "participant_cut_off_no_send"))
	require.Zero(t, AccountingGhostCounterCount(decoded, "participant_state_diverged_no_send"))
}
