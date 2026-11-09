package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. A compose.log holds the terminal line each host writes for the inference it executed, a line
//     with no inference (a receipt the host never signed), a gateway line and a mock-dapi acquire.
//  2. Read the executors.
//  3. Check every inference is named by the host service that wrote its line, without the replica
//     suffix of the log prefix, and nothing else is read as an execution.
func TestReadExecutorsNamesTheHostOfEveryInference(t *testing.T) {
	lines := []string{
		`versiond-2-1         | {"time":"2026-10-19T12:05:19.428748766Z","level":"INFO","msg":"devshard request terminal","request_id":"req-1792411519344903097-3","escrow_id":"1","stage":"terminal","where":"request.terminal","reason":"ok","terminal":"finish_published","inference_id":1,"nonce":1}`,
		`versiond-3-1         | {"time":"2026-10-19T12:05:19.434172921Z","level":"INFO","msg":"devshard request terminal","request_id":"req-1792411519344903097-3","escrow_id":"1","stage":"terminal","where":"request.terminal","reason":"ok","terminal":"finish_published","inference_id":2,"nonce":2}`,
		`versiond-12-1        | {"time":"2026-10-19T12:05:20.143714549Z","level":"INFO","msg":"devshard request terminal","request_id":"req-1792411520095224727-8","escrow_id":"7","stage":"terminal","where":"request.terminal","reason":"ok","terminal":"finish_published","inference_id":1,"nonce":1}`,
		`versiond-2-1         | {"time":"2026-10-19T12:05:56.313743125Z","level":"INFO","msg":"devshard request terminal","request_id":"req-1792411556301301016-11","escrow_id":"1","stage":"terminal","where":"request.terminal","reason":"payload_absent","terminal":"no_receipt_expected","inference_id":0,"nonce":199,"failure_where":"host.sign_receipt"}`,
		`devshardctl-1        | {"time":"2026-10-19T12:05:19.345053858Z","level":"INFO","msg":"gateway_request_received","request":"req-1792411519344903097-3","stage":"gateway_request_received","model":"test-model"}`,
		`mock-dapi-1          | {"time":"2026-10-19T12:05:19.373034477Z","level":"INFO","msg":"mlnode_acquire","request":"req-1792411519344903097-3","stage":"mlnode_acquire","outcome":"acquired","node_id":"mock-openai-0","lock_id":"mock-test-model-1","escrow_id":"1"}`,
		`versiond-2-1         | not a json line`,
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "compose.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	got, err := ReadExecutors(filepath.Join(dir, "compose.log"))
	require.NoError(t, err)
	require.Equal(t, map[InferenceRef]string{
		{Escrow: "1", Inference: 1}: "versiond-2",
		{Escrow: "1", Inference: 2}: "versiond-3",
		{Escrow: "7", Inference: 1}: "versiond-12",
	}, got)
}

// Test flow:
// 1. Read the executors of a run directory that has no compose.log.
// 2. Check the error says the file does not exist, so the caller can leave the executors unnamed.
func TestReadExecutorsWithoutTheLog(t *testing.T) {
	_, err := ReadExecutors(filepath.Join(t.TempDir(), "compose.log"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

// Test flow:
//  1. A compose.log holds the gateway's challenge lines: finished challenges of two outcomes, one
//     that failed, votes, a late reject, a vote cut once the outcome was settled, a voter that
//     stayed busy (429) to the deadline and one that failed otherwise; a host line names a challenge
//     too and is not the gateway's.
//  2. Read the challenge wave.
//  3. Check the outcomes, the reasons and the voters' answers are counted from the gateway alone.
func TestReadChallengeWaveCountsWhatTheGatewayLogged(t *testing.T) {
	gw := func(body string) string { return `devshardctl-1        | ` + body }
	lines := []string{
		gw(`{"time":"2026-10-21T06:23:59.261092355Z","level":"INFO","msg":"challenge_triggered","subsystem":"challenge","escrow":"1","nonce":1,"reason":"empty_stream"}`),
		gw(`{"time":"2026-10-21T06:23:59.261122662Z","level":"INFO","msg":"challenge_triggered","subsystem":"challenge","escrow":"1","nonce":2,"reason":"empty_stream"}`),
		gw(`{"time":"2026-10-21T06:24:09.100000000Z","level":"INFO","msg":"challenge_triggered","subsystem":"challenge","escrow":"1","nonce":3,"reason":"stream_unbound"}`),
		gw(`{"time":"2026-10-21T06:23:59.331693597Z","level":"INFO","msg":"challenge_finished","stage":"challenge_finished","escrow":"1","nonce":1,"reason":"empty_stream","outcome":"none","votes":1,"unanswered":1}`),
		gw(`{"time":"2026-10-21T06:23:59.343523937Z","level":"INFO","msg":"challenge_finished","stage":"challenge_finished","escrow":"1","nonce":2,"reason":"empty_stream","outcome":"none","votes":2,"unanswered":0}`),
		gw(`{"time":"2026-10-21T06:24:10.100000000Z","level":"INFO","msg":"challenge_finished","stage":"challenge_finished","escrow":"1","nonce":3,"reason":"stream_unbound","outcome":"invalidated","votes":3,"unanswered":0}`),
		gw(`{"time":"2026-10-21T06:24:11.100000000Z","level":"WARN","msg":"challenge_failed","stage":"challenge_failed","escrow":"1","nonce":4,"reason":"empty_stream","error":"no verifiers"}`),
		gw(`{"time":"2026-10-21T06:23:59.299618695Z","level":"INFO","msg":"challenge_vote","subsystem":"challenge","escrow":"1","nonce":2,"host":"qnzcwxx2","fault":"none"}`),
		gw(`{"time":"2026-10-21T06:23:59.311169826Z","level":"INFO","msg":"challenge_vote","subsystem":"challenge","escrow":"1","nonce":1,"host":"8qflk2uq","fault":"none"}`),
		gw(`{"time":"2026-10-21T06:23:59.312000000Z","level":"INFO","msg":"challenge_vote_rejected","subsystem":"challenge","escrow":"1","nonce":1,"host":"jen7zehm","reject_cause":"late"}`),
		gw(`{"time":"2026-10-21T06:24:10.000000000Z","level":"INFO","msg":"challenge_vote_not_awaited","subsystem":"challenge","escrow":"1","nonce":3,"host":"hpuxq8je"}`),
		gw(`{"time":"2026-10-21T06:24:10.050000000Z","level":"INFO","msg":"challenge_vote_failed","subsystem":"challenge","escrow":"1","nonce":3,"host":"qnzcwxx2","error":"verify-challenge: resource_exhausted: verifier queue full"}`),
		gw(`{"time":"2026-10-21T06:14:15.96055124Z","level":"INFO","msg":"challenge_vote_failed","subsystem":"challenge","escrow":"1","nonce":2,"host":"jen7zehm","error":"verify-challenge: unimplemented: method is not implemented"}`),
		`versiond-2-1         | {"time":"2026-10-21T06:23:59.300000000Z","level":"INFO","msg":"challenge_vote","escrow":"1","nonce":2}`,
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "compose.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	got, err := ReadChallengeWave(filepath.Join(dir, "compose.log"))
	require.NoError(t, err)
	require.Equal(t, &ChallengeWave{
		Outcomes:   map[string]int{"none": 2, "invalidated": 1, "failed": 1},
		Reasons:    map[string]int{"empty_stream": 3, "stream_unbound": 1},
		Votes:      2,
		Rejected:   map[string]int{"late": 1},
		NotAwaited: 1,
		VoteFailed: 2,
		VoteBusy:   1,
		// Escrow 1: nonce 1 70 ms, nonce 2 82 ms, nonce 3 1000 ms; nonce 4 failed with no trigger logged.
		TriggerToVerdictMs: []int64{70, 82, 1000},
	}, got)
	require.Equal(t, 6, got.Asks())
}

// Test flow:
// 1. A compose.log of a gateway without the challenge (the base) holds no challenge line.
// 2. Read the challenge wave.
// 3. Check there is none, so the report says the wave was not measured instead of zeros.
func TestReadChallengeWaveWithoutChallengeLines(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "compose.log"), []byte(
		`devshardctl-1        | {"msg":"gateway_request_received","stage":"gateway_request_received"}`+"\n"), 0o644))
	got, err := ReadChallengeWave(filepath.Join(dir, "compose.log"))
	require.NoError(t, err)
	require.Nil(t, got)
}
