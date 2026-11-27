package transport

import (
	"bytes"
	"net/http"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

func TestChatRefusesOverCountDiffs(t *testing.T) {
	env := setupServerEnv(t)
	half := bytes.Repeat([]byte{0x12, 0x00}, CatchUpLimits.Txs/2+1) // each diff is under the per-diff cap
	for name, diffs := range map[string][]DiffJSON{
		"diffs": make([]DiffJSON, CatchUpMaxDiffs+1),
		"txs":   {{Nonce: 1, Txs: half}, {Nonce: 2, Txs: half}},
	} {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(InferenceRequest{Diffs: diffs, Nonce: 2, Stream: true})
			require.NoError(t, err)
			rec := env.doPost(t, "/devshard/v2/sessions/escrow-1/chat/completions", body)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), "decode limit")
		})
	}
}
