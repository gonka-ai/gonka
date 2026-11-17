//go:build devshard_testenv

package transport

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/types"
)

func tripExecutorFault(t *testing.T, name string) {
	t.Helper()
	dir := os.Getenv(envTestenvExecutorFaultDir)
	require.NotEmpty(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
	t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, name)) })
}

func faultInferenceBody(t *testing.T, env *serverTestEnv) []byte {
	t.Helper()
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj, err := DiffToJSON(diff)
	require.NoError(t, err)
	body, err := json.Marshal(InferenceRequest{
		Diffs: []DiffJSON{dj},
		Nonce: 1,
		Payload: &PayloadJSON{
			Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100,
			MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
		},
	})
	require.NoError(t, err)
	return body
}

func sseReceipt(t *testing.T, body string) DevshardReceiptEvent {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal([]byte(data), &envelope) != nil {
			continue
		}
		if raw, ok := envelope["devshard_receipt"]; ok {
			var ev DevshardReceiptEvent
			require.NoError(t, json.Unmarshal(raw, &ev))
			return ev
		}
	}
	t.Fatalf("no devshard_receipt event in %q", body)
	return DevshardReceiptEvent{}
}

func TestExecutorFault_DropPayloadSignsStateWithoutReceipt(t *testing.T) {
	t.Setenv(envTestenvExecutorFaultDir, t.TempDir())
	env := setupServerEnv(t)
	tripExecutorFault(t, ExecutorFaultDropPayloadFile)

	rec := env.doPost(t, "/devshard/v2/sessions/escrow-1/chat/completions", faultInferenceBody(t, env))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	receipt := sseReceipt(t, rec.Body.String())
	require.Empty(t, receipt.Receipt, "a dropped payload signs no receipt")
	require.NotEmpty(t, receipt.StateSig, "the diff still applies and the state is signed")
	require.Empty(t, host.RecoveryTxsFor(env.server.host.MempoolTxs(), 1), "nothing is queued for the inference")
	_, ok := env.server.host.SnapshotState().Inferences[1]
	require.True(t, ok, "the start diff applied")
}

func TestExecutorFault_ForgedChallengeFailsEvidenceChecks(t *testing.T) {
	t.Setenv(envTestenvExecutorFaultDir, t.TempDir())
	env := setupServerEnv(t)
	rec := env.doPost(t, "/devshard/v2/sessions/escrow-1/chat/completions", faultInferenceBody(t, env))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, sseReceipt(t, rec.Body.String()).Receipt, "fault off: the host signs a receipt")

	tripExecutorFault(t, ExecutorFaultForgeChallengeFile)
	resp, err := env.server.ServeChallengeReceipt(t.Context(), ChallengeReceiptRequest{InferenceID: 1})
	require.NoError(t, err)
	require.Equal(t, ForgedChallengeSig, resp.Receipt)
	txs, err := DevshardTxsFromBytes(resp.Mempool)
	require.NoError(t, err)
	require.Len(t, txs, 2)

	record := env.server.host.SnapshotState().Inferences[1]
	confirm := txs[0].GetConfirmStart()
	require.NotNil(t, confirm)
	require.Equal(t, uint64(1), confirm.InferenceId)
	require.Equal(t, resp.Receipt, confirm.ExecutorSig, "the forged confirm matches the forged receipt, so verifiers reach the signature check")
	require.ErrorIs(t, env.server.host.CheckEvidence(record, txs[0]), types.ErrInvalidExecutorSig)

	finish := txs[1].GetFinishInference()
	require.NotNil(t, finish)
	require.Equal(t, "escrow-1", finish.EscrowId)
	require.Equal(t, record.ExecutorSlot, finish.ExecutorSlot)
	started := *record
	started.Status = types.StatusStarted
	require.Error(t, env.server.host.CheckEvidence(&started, txs[1]))
}
