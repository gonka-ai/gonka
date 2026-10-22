package harness

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/testenv/mockopenai"
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

func writeLayersRun(t *testing.T, files map[string][]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, lines := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	}
	return dir
}

// Test flow:
//  1. A mock-dapi journal: locks released, one released before its acquire line, two never released,
//     a failed acquire, one taken within the grace of the journal end, and a gateway line that looks
//     like an acquire.
//  2. Read the layers and check them.
//  3. Check the node that leaked is named with its first lost lock and the kinds of request that
//     held them, the lock near the end is not judged, and a run without the journal is not measured.
func TestLockBalanceCountsLocksThatNeverCameBack(t *testing.T) {
	dir := writeLayersRun(t, map[string][]string{"compose.log": {
		`mock-dapi-1  | {"time":"2026-10-19T07:58:00Z","msg":"mlnode_acquire","outcome":"acquired","lock_id":"a","node_id":"n4","request_id":"req-1"}`,
		`mock-dapi-1  | {"time":"2026-10-19T07:58:01Z","msg":"mlnode_release","outcome":"SUCCESS","lock_id":"a","node_id":"n4"}`,
		// Released before its acquire line reached the log: still closed.
		`mock-dapi-1  | {"time":"2026-10-19T07:58:02Z","msg":"mlnode_release","outcome":"TRANSPORT_ERROR","lock_id":"b","node_id":"n4"}`,
		`mock-dapi-1  | {"time":"2026-10-19T07:58:02Z","msg":"mlnode_acquire","outcome":"acquired","lock_id":"b","node_id":"n4","request_id":"req-2"}`,
		`mock-dapi-1  | {"time":"2026-10-19T07:59:00Z","msg":"mlnode_acquire","outcome":"acquired","lock_id":"c","node_id":"n4","request_id":"req-3"}`,
		`mock-dapi-1  | {"time":"2026-10-19T07:59:05Z","msg":"mlnode_acquire","outcome":"acquired","lock_id":"d","node_id":"n4","request_id":"validate-7"}`,
		`mock-dapi-1  | {"time":"2026-10-19T07:59:06Z","msg":"mlnode_acquire","outcome":"no_nodes_available","node_id":""}`,
		`mock-dapi-1  | {"time":"2026-10-19T07:59:07Z","msg":"mlnode_acquire","outcome":"acquired","lock_id":"e","node_id":"n0","request_id":"req-4"}`,
		`mock-dapi-1  | {"time":"2026-10-19T07:59:08Z","msg":"mlnode_release","outcome":"SUCCESS","lock_id":"e","node_id":"n0"}`,
		// Taken within the grace of the journal end: may still be released.
		`mock-dapi-1  | {"time":"2026-10-19T08:05:00Z","msg":"mlnode_acquire","outcome":"acquired","lock_id":"f","node_id":"n4","request_id":"req-5"}`,
		`devshardctl-1 | {"time":"2026-10-19T08:05:00Z","msg":"mlnode_acquire","outcome":"acquired","lock_id":"x","node_id":"n9"}`,
	}})
	layers, err := ReadLoadtestLayers(dir)
	require.NoError(t, err)

	rep := CheckPromises(PromiseInput{Layers: layers, LockGrace: 3 * time.Minute})
	got := violations(rep, NodeLocksReleased)
	require.Len(t, got, 1, "one node leaked")
	require.Equal(t, "n4", got[0].Node)
	require.Equal(t, "req-3", got[0].RequestID, "the first lock that never came back")
	require.Contains(t, got[0].Detail, "2 of 5 locks on n4 never released")
	require.Contains(t, got[0].Detail, "map[req:1 validate:1]")
	require.True(t, rep.Checked[NodeLocksReleased].Measured)
	require.Contains(t, rep.Checked[NodeLocksReleased].Note, "n0 1/1, n4 2/5; 1 taken within 3m0s")

	rep = CheckPromises(PromiseInput{})
	require.Contains(t, rep.Checked[NodeLocksReleased].Note, "no mock-dapi lock journal")
}

// Test flow:
//  1. The gateway burns nine nonces on one host in a minute of ten requests and one on another host
//     in a minute of one request.
//  2. Check without an allowance: counted, no verdict, the busy minute observed.
//  3. Check a run whose gateway logged no host label: not measured rather than zero burns.
//  4. Check with an allowance of 0.95 per request: only the minute of one burn per request breaks it.
func TestNonceBurnIsCountedPerHostAndMinuteAgainstRequests(t *testing.T) {
	burn := func(at, host, reason string) string {
		return `devshardctl-1 | {"time":"` + at + `","stage":"ghost_probe_skipped","escrow":"1","nonce":7,"host":"` + host + `","reason":"` + reason + `"}`
	}
	var requests []string
	for i := 0; i < 10; i++ {
		requests = append(requests, `{"request_id":"r","started_at":"2026-10-19T08:44:0`+string(rune('0'+i))+`Z"}`)
	}
	requests = append(requests, `{"request_id":"r","started_at":"2026-10-19T08:45:00Z"}`)
	var log []string
	for i := 0; i < 9; i++ {
		log = append(log, burn("2026-10-19T08:44:30Z", "9ph0es38", "participant_throttled_no_send"))
	}
	log = append(log, burn("2026-10-19T08:45:10Z", "czpp065f", "no_compatible_request_after_stale"))
	dir := writeLayersRun(t, map[string][]string{"compose.log": log, "requests.jsonl": requests})
	layers, err := ReadLoadtestLayers(dir)
	require.NoError(t, err)

	rep := CheckPromises(PromiseInput{Layers: layers})
	require.False(t, rep.Checked[OneNoncePerRequest].Measured, "no allowance, no verdict")
	require.Contains(t, rep.Checked[OneNoncePerRequest].Note, "10 burned nonces over 2 host-minutes; worst 1.00 per request")
	require.Contains(t, strings.Join(rep.Observations, "\n"), "host 9ph0es38 08:44 — 9 nonces burned without sending against 10 requests (0.90 per request)")

	rep = CheckPromises(PromiseInput{Layers: &LoadtestLayers{RequestsPerMinute: layers.RequestsPerMinute}})
	require.Contains(t, rep.Checked[OneNoncePerRequest].Note, "no gateway log line with a host label", "a renamed stage is not zero burns")

	rep = CheckPromises(PromiseInput{Layers: layers, BurnAllowance: 0.95})
	got := violations(rep, OneNoncePerRequest)
	require.Len(t, got, 1, "0.90 per request is within 0.95; 1 burn against 1 request is not")
	require.Equal(t, "czpp065f", got[0].Host)
}

const (
	honestParticipant  = "gonka1qzwyhs83g7rwpwhfveajtszgayp0c95sck5r4l"
	faultedParticipant = "gonka1xcs7z67n940ku8mlzmp6m8ghp4adxjk55fm9k4"
)

func hostIdentity(service, participant string) string {
	return service + "  | {\"time\":\"2026-10-19T07:50:00Z\",\"level\":\"INFO\",\"msg\":\"devshardd: validation lease identity\",\"instance_address\":\"" + participant + "\"}"
}

func gatewayLine(at, msg string) string {
	return `devshardctl-1 | {"time":"` + at + `","level":"INFO","msg":"` + msg + `"}`
}

// Test flow:
//  1. compose.log: the hosts name their participants; the gateway quarantines the honest participant
//     after three empty streams on it, quarantines the faulted one, and ends a quarantine.
//     host-faults.json: the runner paused the faulted participant's host around its quarantine.
//  2. Read the layers and check them.
//  3. Check only the honest participant's quarantine is a violation, with what the gateway held
//     against it in the minute before; the end of a quarantine is not one.
//  4. Check a run whose gateway quarantined nobody holds, and one with no gateway log is not measured.
func TestQuarantineOutsideTheFaultWindowCarriesItsCause(t *testing.T) {
	strike := func(at, host string, bytes int) string {
		return `devshardctl-1 | {"time":"` + at + `","msg":"empty_stream","stage":"empty_stream","escrow":"4","host":"` + host + `","stream_bytes_read":` + strconv.Itoa(bytes) + `}`
	}
	dir := writeLayersRun(t, map[string][]string{
		"compose.log": {
			hostIdentity("versiond-3-1", honestParticipant),
			hostIdentity("versiond-4-1", faultedParticipant),
			strike("2026-10-19T07:59:13.984000001Z", "5sck5r4l", 913),
			strike("2026-10-19T07:59:13.984000002Z", "5sck5r4l", 1135),
			strike("2026-10-19T07:59:13.984000003Z", "5sck5r4l", 0),
			strike("2026-10-19T07:57:00Z", "5sck5r4l", 10), // outside the minute before
			gatewayLine("2026-10-19T07:59:18Z", "participant_limit_empty_stream_quarantine participant_key="+honestParticipant+
				` model_id=\"m\" reason=empty_stream strikes=3 threshold=3 quarantine_mode=shadow`),
			gatewayLine("2026-10-19T07:59:20Z", "participant_limit_transport_failure participant_key="+faultedParticipant+
				` path_kind=chat error=\"connection refused\"`),
			gatewayLine("2026-10-19T08:09:18Z", "participant_limit_expired participant_key="+honestParticipant),
		},
		"host-faults.json": {`[{"host":"versiond-4","mode":"pause","start_after":"30s","duration":"1m45s","paused_at":"2026-10-19T07:58:54Z","resumed_at":"2026-10-19T08:00:39Z"}]`},
	})
	layers, err := ReadLoadtestLayers(dir)
	require.NoError(t, err)
	require.Len(t, layers.Transitions, 2, "the expiry is not a quarantine")

	rep := CheckPromises(PromiseInput{Layers: layers})
	got := violations(rep, QuarantineOnlyWhenFaulted)
	require.Len(t, got, 1, "the faulted participant's quarantine falls in its window")
	require.Equal(t, "5sck5r4l", got[0].Host)
	require.Contains(t, got[0].Detail, "shadow quarantine (empty_stream_quarantine) of "+honestParticipant+" at 2026-10-19T07:59:18Z")
	require.Contains(t, got[0].Detail, "map[empty_stream:3]; 2 of 3 empty streams had bytes read; escrows map[4:3]; largest burst 3 in one millisecond")
	require.Equal(t, 2, rep.Checked[QuarantineOnlyWhenFaulted].Judged)

	quiet := &LoadtestLayers{GatewayLines: 1}
	require.True(t, CheckPromises(PromiseInput{Layers: quiet}).Checked[QuarantineOnlyWhenFaulted].Measured, "a run without quarantines passes")
	require.Contains(t, CheckPromises(PromiseInput{Layers: &LoadtestLayers{}}).Checked[QuarantineOnlyWhenFaulted].Note, "no gateway log",
		"a run whose gateway logged nothing is not zero quarantines")
}

// Test flow:
//  1. host-faults.json: the runner paused versiond-2 for a minute and failed to pause versiond-3;
//     compose.log names versiond-2's participant and the gateway quarantines it twice, during the
//     pause and 3m03s after it. An events.json from another runner puts the pause later.
//  2. Read the layers and check them.
//  3. Check the windows come from host-faults.json only: one window (a failed pause opens none), the
//     quarantine during the pause is the fault's, the one past the grace (2m35s with no timeouts
//     known) is a violation.
func TestFaultWindowsComeFromHostFaults(t *testing.T) {
	const paused = "gonka1khwza4jkftj0qyhyw4fhd6ev4584hy9ph0es38"
	quarantine := func(at string) string {
		return gatewayLine(at, "participant_limit_transport_failure participant_key="+paused+` path_kind=chat error=\"EOF\"`)
	}
	dir := writeLayersRun(t, map[string][]string{
		"compose.log": {
			hostIdentity("versiond-2-1", paused),
			quarantine("2026-10-19T08:43:47Z"),
			quarantine("2026-10-19T08:47:30Z"),
		},
		"host-faults.json": {`[` +
			`{"host":"versiond-2","mode":"pause","start_after":"1m","duration":"1m","paused_at":"2026-10-19T08:43:27Z","resumed_at":"2026-10-19T08:44:27Z"},` +
			`{"host":"versiond-3","mode":"pause","start_after":"1m","duration":"1m","error":"no such container"}]`},
		"events.json": {`[{"id":"host_faults-1","action":"pause","target":"versiond-2","started_at":"2026-10-19T08:46:00Z","ended_at":"2026-10-19T08:48:00Z"}]`},
	})
	layers, err := ReadLoadtestLayers(dir)
	require.NoError(t, err)
	require.Len(t, layers.FaultWindows, 1, "a pause that failed opens no window")
	require.Equal(t, paused, layers.FaultWindows[0].Participant, "the host is tied to its participant through its identity line")
	require.Equal(t, "versiond-2", layers.FaultWindows[0].Host)

	got := violations(CheckPromises(PromiseInput{Layers: layers}), QuarantineOnlyWhenFaulted)
	require.Len(t, got, 1, "the quarantine during the pause is the fault's; the one 3m03s after it ends is not")
	require.Contains(t, got[0].Detail, "at 2026-10-19T08:47:30Z")
	require.Contains(t, got[0].Detail, "probe quarantine (transport_failure_quarantine)")

	layers.FaultWindows[0].Participant = ""
	rep := CheckPromises(PromiseInput{Layers: layers})
	require.Contains(t, rep.Checked[QuarantineOnlyWhenFaulted].Note, "the runner paused versiond-2, whose participant compose.log does not name",
		"a pause that cannot be tied to a participant is not judged as no pause")
	require.Empty(t, violations(rep, QuarantineOnlyWhenFaulted))
}

// Test flow:
//  1. A gateway quarantine outside any fault window in a run whose ML node answered with chaos.
//  2. Check the quarantine check does not judge the run: the dishonest answers may have caused it.
//  3. Check the same run with every answer honest judges the quarantine and breaks.
func TestQuarantineIsNotJudgedWhenTheMLAnsweredDishonestly(t *testing.T) {
	dir := writeLayersRun(t, map[string][]string{"compose.log": {
		gatewayLine("2026-10-19T07:59:18Z", "participant_limit_activated participant_key="+honestParticipant+" status=503 path_kind=chat"),
	}})
	layers, err := ReadLoadtestLayers(dir)
	require.NoError(t, err)
	in := PromiseInput{Layers: layers, Mock: []mockopenai.RequestRecord{
		{Phase: mockopenai.PhaseExecute, Chaos: mockopenai.ChaosOK},
		{Phase: mockopenai.PhaseExecute, Chaos: mockopenai.ChaosEmpty},
	}}
	rep := CheckPromises(in)
	require.False(t, rep.Checked[QuarantineOnlyWhenFaulted].Measured)
	require.Contains(t, rep.Checked[QuarantineOnlyWhenFaulted].Note, "the ML node answered dishonestly (chaos empty)")
	require.Empty(t, violations(rep, QuarantineOnlyWhenFaulted))

	in.Mock[1].Chaos = mockopenai.ChaosOK
	got := violations(CheckPromises(in), QuarantineOnlyWhenFaulted)
	require.Len(t, got, 1)
	require.Contains(t, got[0].Detail, "probe quarantine (status 503)")
}

// Test flow:
//  1. A host burns 1.5 nonces per request in a minute.
//  2. Check it with a negative allowance and with NaN.
//  3. Check neither is taken as an allowance the minute stays within: no verdict instead of "held".
func TestNonceBurnNegativeOrNaNAllowanceIsNotAVerdict(t *testing.T) {
	m := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	layers := &LoadtestLayers{
		RequestsPerMinute: map[time.Time]int{m: 2},
		GatewayHostLines:  1,
		Burns: []NonceBurn{
			{At: m.Add(time.Second), Escrow: "1", Host: "abcdefgh", Reason: "ghost_probe_skipped"},
			{At: m.Add(2 * time.Second), Escrow: "1", Host: "abcdefgh", Reason: "ghost_probe_skipped"},
			{At: m.Add(3 * time.Second), Escrow: "1", Host: "abcdefgh", Reason: "ghost_probe_skipped"},
		},
	}
	for _, allowance := range []float64{-1, math.NaN()} {
		rep := PromiseReport{Checked: map[string]Measure{}}
		checkNonceBurn(PromiseInput{Layers: layers, BurnAllowance: allowance}, &rep)
		require.NotEqual(t, "held", PromiseResult(rep, OneNoncePerRequest), "allowance %v: 1.5 burns per request judged against nothing", allowance)
	}
}

// Test flow:
//  1. mock-dapi hands out lock mock-m-1 twice (its counter restarted): the first comes back, the
//     second never does; another lock comes back near the journal end.
//  2. Read the layers and check them.
//  3. Check the second lock under the reused id is a leak of its own, named by its request and node.
func TestLockBalanceTellsAReusedLockIDFromTheFirst(t *testing.T) {
	dir := writeLayersRun(t, map[string][]string{"compose.log": {
		`mock-dapi-1  | {"time":"2026-10-19T07:50:00Z","msg":"mlnode_acquire","outcome":"acquired","lock_id":"mock-m-1","node_id":"n0","request_id":"req-1"}`,
		`mock-dapi-1  | {"time":"2026-10-19T07:50:01Z","msg":"mlnode_release","outcome":"SUCCESS","lock_id":"mock-m-1","node_id":"n0"}`,
		`mock-dapi-1  | {"time":"2026-10-19T07:55:00Z","msg":"mlnode_acquire","outcome":"acquired","lock_id":"mock-m-1","node_id":"n0","request_id":"req-2"}`,
		`mock-dapi-1  | {"time":"2026-10-19T08:05:00Z","msg":"mlnode_acquire","outcome":"acquired","lock_id":"mock-m-2","node_id":"n0","request_id":"req-3"}`,
		`mock-dapi-1  | {"time":"2026-10-19T08:05:01Z","msg":"mlnode_release","outcome":"SUCCESS","lock_id":"mock-m-2","node_id":"n0"}`,
	}})
	layers, err := ReadLoadtestLayers(dir)
	require.NoError(t, err)
	rep := CheckPromises(PromiseInput{Layers: layers, LockGrace: 3 * time.Minute})
	got := violations(rep, NodeLocksReleased)
	require.Len(t, got, 1, "the second lock under mock-m-1 never came back")
	require.Equal(t, "req-2", got[0].RequestID)
	require.Contains(t, got[0].Detail, "1 of 3 locks on n0 never released")
	require.Empty(t, got[0].Host, "an ML node is not a devshard host")
	require.Equal(t, "n0", got[0].Node)
}

// Test flow:
//  1. A host burns a nonce in a minute of ten requests and one in a later minute without requests;
//     the generator dropped a request in the first minute.
//  2. Check with an allowance.
//  3. Check only the minute compared against requests is judged, the burn of the empty minute is
//     reported, and the dropped request is not one the gateway saw.
func TestNonceBurnCountsOnlyTheMinutesItCompared(t *testing.T) {
	burn := func(at string) string {
		return `devshardctl-1 | {"time":"` + at + `","stage":"ghost_probe_skipped","escrow":"1","nonce":7,"host":"9ph0es38","reason":"participant_throttled_no_send"}`
	}
	var requests []string
	for i := 0; i < 10; i++ {
		requests = append(requests, `{"request_id":"r","started_at":"2026-10-19T08:44:0`+strconv.Itoa(i)+`Z","outcome":"completed"}`)
	}
	requests = append(requests, `{"request_id":"d","started_at":"2026-10-19T08:44:30Z","outcome":"dropped_by_generator"}`)
	dir := writeLayersRun(t, map[string][]string{
		"compose.log":    {burn("2026-10-19T08:44:10Z"), burn("2026-10-19T08:46:10Z")},
		"requests.jsonl": requests,
	})
	layers, err := ReadLoadtestLayers(dir)
	require.NoError(t, err)
	require.Equal(t, 10, layers.RequestsPerMinute[time.Date(2026, 10, 19, 8, 44, 0, 0, time.UTC)], "a dropped request never reached the gateway")
	rep := CheckPromises(PromiseInput{Layers: layers, BurnAllowance: 0.5})
	require.Equal(t, 1, rep.Checked[OneNoncePerRequest].Judged)
	require.Contains(t, strings.Join(rep.Observations, "\n"), "host 9ph0es38 08:46 — 1 nonces burned without sending in a minute without requests")
}

// Test flow:
//  1. The runner paused a host; the gateway burns nonces of that host during the pause and of
//     another host outside any window, with an allowance both minutes exceed.
//  2. Check the run.
//  3. Check only the burns outside the fault window are judged.
func TestNonceBurnLeavesOutTheFaultWindows(t *testing.T) {
	m := time.Date(2026, 10, 19, 8, 44, 0, 0, time.UTC)
	layers := &LoadtestLayers{
		RequestsPerMinute: map[time.Time]int{m: 1},
		GatewayHostLines:  1,
		Burns: []NonceBurn{
			{At: m.Add(10 * time.Second), Host: hostLabel(faultedParticipant)},
			{At: m.Add(11 * time.Second), Host: hostLabel(faultedParticipant)},
			{At: m.Add(12 * time.Second), Host: hostLabel(honestParticipant)},
			{At: m.Add(13 * time.Second), Host: hostLabel(honestParticipant)},
		},
		FaultWindows: []FaultWindow{{Host: "versiond-4", Participant: faultedParticipant, From: m, To: m.Add(time.Minute), GracedTo: m.Add(3 * time.Minute)}},
	}
	rep := CheckPromises(PromiseInput{Layers: layers, BurnAllowance: 1})
	got := violations(rep, OneNoncePerRequest)
	require.Len(t, got, 1, "the paused host's burns are the fault's")
	require.Equal(t, hostLabel(honestParticipant), got[0].Host)
	require.Contains(t, rep.Checked[OneNoncePerRequest].Note, "2 within host fault windows not judged")
}

// Test flow:
//  1. A gateway quarantine outside any fault window in runs whose ML node failed without chaos: a
//     fault rule answered with a status, a node hung, a node delayed its answers.
//  2. Check each run.
//  3. Check the quarantine check does not judge them, and names the node's failure rather than a
//     dishonest answer.
func TestQuarantineIsNotJudgedWhenTheMLNodeFailed(t *testing.T) {
	dir := writeLayersRun(t, map[string][]string{"compose.log": {
		gatewayLine("2026-10-19T07:59:18Z", "participant_limit_activated participant_key="+honestParticipant+" status=503 path_kind=chat"),
	}})
	layers, err := ReadLoadtestLayers(dir)
	require.NoError(t, err)
	for _, rec := range []mockopenai.RequestRecord{
		{Phase: mockopenai.PhaseExecute, Outcome: "rule_status", Rule: "execution-busy", Status: 503},
		{Phase: mockopenai.PhaseExecute, Outcome: "fault_hang"},
		{Phase: mockopenai.PhaseExecute, Chaos: mockopenai.ChaosDelay, Outcome: "ok"},
	} {
		rep := CheckPromises(PromiseInput{Layers: layers, Mock: []mockopenai.RequestRecord{{Phase: mockopenai.PhaseExecute, Outcome: "ok"}, rec}})
		require.False(t, rep.Checked[QuarantineOnlyWhenFaulted].Measured, "%+v", rec)
		require.Contains(t, rep.Checked[QuarantineOnlyWhenFaulted].Note, "the ML node failed", "%+v", rec)
		require.Empty(t, violations(rep, QuarantineOnlyWhenFaulted))
	}
}

// Test flow:
//  1. The gateway logs a burst of transport-failure quarantines of one participant (one per request
//     in flight), then another a minute later.
//  2. Check the run.
//  3. Check the burst is one quarantine, judged and reported once with its line count.
func TestQuarantineBurstIsOneQuarantine(t *testing.T) {
	q := func(at string) string {
		return gatewayLine(at, "participant_limit_transport_failure participant_key="+honestParticipant+` path_kind=chat error=\"EOF\"`)
	}
	dir := writeLayersRun(t, map[string][]string{"compose.log": {
		q("2026-10-19T07:59:18.001Z"), q("2026-10-19T07:59:18.005Z"), q("2026-10-19T07:59:18.020Z"),
		q("2026-10-19T08:00:30Z"),
	}})
	layers, err := ReadLoadtestLayers(dir)
	require.NoError(t, err)
	rep := CheckPromises(PromiseInput{Layers: layers})
	got := violations(rep, QuarantineOnlyWhenFaulted)
	require.Len(t, got, 2)
	require.Contains(t, got[0].Detail, "3 lines")
	require.Equal(t, 2, rep.Checked[QuarantineOnlyWhenFaulted].Judged)
}

// Test flow:
//  1. The runner paused a host and resumed it; the gateway quarantines its participant 2m40s after
//     the resume, in a run whose escrows have refusal 5s and execution 30s.
//  2. Read the layers, give the windows the grace of the run's timeouts and check them.
//  3. Check the quarantine falls in the grace: a host just back keeps its in-flight inferences until
//     their timeouts are voted and swept.
func TestFaultGraceCoversTheTimeoutsOfTheRun(t *testing.T) {
	dir := writeLayersRun(t, map[string][]string{
		"compose.log": {
			hostIdentity("versiond-2-1", faultedParticipant),
			gatewayLine("2026-10-19T08:47:07Z", "participant_limit_transport_failure participant_key="+faultedParticipant+` path_kind=chat error=\"EOF\"`),
		},
		"host-faults.json": {`[{"host":"versiond-2","mode":"pause","paused_at":"2026-10-19T08:43:27Z","resumed_at":"2026-10-19T08:44:27Z"}]`},
	})
	layers, err := ReadLoadtestLayers(dir)
	require.NoError(t, err)
	layers.GraceFaultWindows(map[string]EscrowTimeouts{"1": {Refusal: 5, Execution: 30}, "2": {Refusal: 5, Execution: 20}})
	require.Equal(t, 3*time.Minute+10*time.Second, layers.FaultWindows[0].GracedTo.Sub(layers.FaultWindows[0].To))
	require.Empty(t, violations(CheckPromises(PromiseInput{Layers: layers}), QuarantineOnlyWhenFaulted))
}

// Test flow:
//  1. The runner paused a host and never saw it resumed; the gateway quarantines its participant
//     before the pause and long after it.
//  2. Check the run.
//  3. Check the open window covers the late quarantine and the one before the pause is a violation.
func TestQuarantineAgainstAnOpenFaultWindow(t *testing.T) {
	q := func(at string) string {
		return gatewayLine(at, "participant_limit_transport_failure participant_key="+faultedParticipant+` path_kind=chat error=\"EOF\"`)
	}
	dir := writeLayersRun(t, map[string][]string{
		"compose.log":      {hostIdentity("versiond-2-1", faultedParticipant), q("2026-10-19T08:40:00Z"), q("2026-10-19T09:40:00Z")},
		"host-faults.json": {`[{"host":"versiond-2","mode":"pause","paused_at":"2026-10-19T08:43:27Z"}]`},
	})
	layers, err := ReadLoadtestLayers(dir)
	require.NoError(t, err)
	got := violations(CheckPromises(PromiseInput{Layers: layers}), QuarantineOnlyWhenFaulted)
	require.Len(t, got, 1)
	require.Contains(t, got[0].Detail, "at 2026-10-19T08:40:00Z", "a quarantine before the pause is not the pause's")
}

// Test flow:
//  1. Two host services of one participant (an HA pair) name the same address; the runner pauses one.
//  2. Check a quarantine of that participant during the pause.
//  3. Check the pause of either service covers its participant.
func TestQuarantineOfAnHAPairDuringThePauseOfOne(t *testing.T) {
	dir := writeLayersRun(t, map[string][]string{
		"compose.log": {
			hostIdentity("versiond-0-1", faultedParticipant),
			hostIdentity("versiond-1-1", faultedParticipant),
			gatewayLine("2026-10-19T08:44:00Z", "participant_limit_transport_failure participant_key="+faultedParticipant+` path_kind=chat error=\"EOF\"`),
		},
		"host-faults.json": {`[{"host":"versiond-1","mode":"pause","paused_at":"2026-10-19T08:43:27Z","resumed_at":"2026-10-19T08:44:27Z"}]`},
	})
	layers, err := ReadLoadtestLayers(dir)
	require.NoError(t, err)
	require.Empty(t, violations(CheckPromises(PromiseInput{Layers: layers}), QuarantineOnlyWhenFaulted))
}
