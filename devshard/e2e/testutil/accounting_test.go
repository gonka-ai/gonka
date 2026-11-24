package testutil

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Decode one participant row carrying a devshardctl counter, keyed under "key".
//  2. Decode one participant row carrying a gateway counter, keyed flat beside the count.
//  3. Assert both counters read the same disposition and slot, and both count as a ghost burn of their reason.
func TestAccountingCounter_ReadsBothCounterShapes(t *testing.T) {
	const nested = `{"participants":[{"counters":[{"escrow_id":"1","key":{"slot_id":2,"disposition":"ghost","no_send_reason":"participant_cut_off_no_send"},"count":3}]}]}`
	const flat = `{"participants":[{"counters":[{"escrow_id":"1","slot_id":2,"disposition":"ghost","ghost_reason":"participant_cut_off_no_send","count":3}]}]}`

	for name, body := range map[string]string{"nested": nested, "flat": flat} {
		var decoded AccountingParticipantsResponse
		require.NoError(t, json.Unmarshal([]byte(body), &decoded), name)
		counter := decoded.Participants[0].Counters[0]
		require.Equal(t, "1", counter.EscrowID, name)
		require.Equal(t, uint32(2), counter.Key.SlotID, name)
		require.Equal(t, "ghost", counter.Key.Disposition, name)
		require.Equal(t, uint64(3), counter.Count, name)
		require.Equal(t, uint64(3), AccountingGhostCounterCount(decoded, "participant_cut_off_no_send", ""), name)
	}
}
