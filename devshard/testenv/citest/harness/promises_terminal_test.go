package harness

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard"
	"devshard/state"
	"devshard/testenv/mockopenai"
	"devshard/types"
)

var t0 = time.Unix(1_791_700_000, 0)

func TestPromiseDeadlineJudgedPerHost(t *testing.T) {
	started := func(finished bool) *InferenceFacts {
		return &InferenceFacts{Started: true, StartedAt: t0.Unix(), Confirmed: true, ConfirmedAt: t0.Unix() + 1, Finished: finished}
	}
	in := PromiseInput{
		Diffs: ExportDiffs{Hosts: []HostChain{
			hostChain("a", map[uint64]*InferenceFacts{1: started(true)}, nil),
			hostChain("b", map[uint64]*InferenceFacts{1: started(false)}, nil),
		}},
		Timeouts: map[string]EscrowTimeouts{"2": {Refusal: 60, Execution: 60}},
	}
	// Deadline = confirmed + 60 s + 5 s + 120 s + 30 s.
	in.ObservedUntil = t0.Add(200 * time.Second)
	rep := CheckPromises(in)
	require.Empty(t, violations(rep, InferenceClosesInTime), "within the deadline is allowed")
	require.Equal(t, 1, rep.Checked[InferenceClosesInTime].Judged, "host a's Finish is final; host b is still within its deadline")
	require.Len(t, rep.Open, 1)

	in.ObservedUntil = t0.Add(300 * time.Second)
	rep = CheckPromises(in)
	got := violations(rep, InferenceClosesInTime)
	require.Len(t, got, 1, "host b still holds the inference open; host a's Finish must not hide it")
	require.Equal(t, "b", got[0].Host)
	require.True(t, rep.Checked[InferenceClosesInTime].Measured)
}

func TestPromisePendingUsesRefusalTimeout(t *testing.T) {
	in := PromiseInput{
		Diffs:         ExportDiffs{Hosts: []HostChain{hostChain("a", map[uint64]*InferenceFacts{1: {Started: true, StartedAt: t0.Unix()}}, nil)}},
		Timeouts:      map[string]EscrowTimeouts{"2": {Refusal: 60, Execution: 1920}},
		ObservedUntil: t0.Add(200 * time.Second),
	}
	require.Empty(t, violations(CheckPromises(in), InferenceClosesInTime), "60 s + 155 s of timeout grace not over yet")
	in.ObservedUntil = t0.Add(220 * time.Second)
	require.Len(t, violations(CheckPromises(in), InferenceClosesInTime), 1, "a Pending inference is due at refusal_timeout, not execution_timeout")
}

// Test flow:
//  1. A Pending inference carrying the probe prompt the gateway burns a nonce with, built as
//     pocProbePromptBody of cmd/devshardctl/poc_mode.go builds it, never confirmed.
//  2. Judge it an hour past its refusal timeout.
//  3. Check it is counted as a burned nonce, not as a deadline violation.
func TestPromiseGhostProbeIsLeftToSettlement(t *testing.T) {
	probe, err := devshard.CanonicalPromptHash([]byte(fmt.Sprintf(`{"messages":[{"role":"user","content":"."}],"max_tokens":%d}`, uint64(state.MinReservationTokens))))
	require.NoError(t, err)
	ghost := &InferenceFacts{Started: true, StartedAt: t0.Unix(), PromptHash: probe}
	in := PromiseInput{
		Diffs:         ExportDiffs{Hosts: []HostChain{hostChain("a", map[uint64]*InferenceFacts{1: ghost}, nil)}},
		Timeouts:      map[string]EscrowTimeouts{"2": {Refusal: 60, Execution: 60}},
		ObservedUntil: t0.Add(time.Hour),
	}
	rep := CheckPromises(in)
	require.Empty(t, violations(rep, InferenceClosesInTime), "a burned nonce is never sent, so nothing times it out")
	require.Contains(t, rep.Checked[InferenceClosesInTime].Note, "1 burned nonces")
}

func TestPromiseServedFinishOnEveryHost(t *testing.T) {
	in := PromiseInput{
		Diffs: ExportDiffs{Hosts: []HostChain{
			hostChain("a", map[uint64]*InferenceFacts{1: {Started: true, Finished: true}, 2: {Started: true, Finished: true}}, nil),
			hostChain("b", map[uint64]*InferenceFacts{1: {Started: true, Finished: true}, 2: {Started: true}}, nil),
		}},
		Requests: []ClientRequest{served(1, 200), served(2, 200), served(3, 502)},
	}
	cut := served(4, 200)
	cut.Incomplete = true
	in.Requests = append(in.Requests, cut)
	in.Timeouts = map[string]EscrowTimeouts{"2": {Refusal: 60, Execution: 60}}
	for _, c := range in.Diffs.Hosts {
		for _, f := range c.Inferences {
			f.StartedAt, f.ConfirmedAt, f.Confirmed = t0.Unix(), t0.Unix(), true
		}
	}

	in.ObservedUntil = t0.Add(time.Minute)
	rep := CheckPromises(in)
	require.Empty(t, violations(rep, ServedRequestFinished), "a Finish still within the deadline may be in flight")
	require.Equal(t, 1, rep.Checked[ServedRequestFinished].Judged, "request 1 has its Finish on both hosts; request 2 still lacks one on b")

	in.ObservedUntil = t0.Add(time.Hour)
	rep = CheckPromises(in)
	got := violations(rep, ServedRequestFinished)
	require.Len(t, got, 1)
	require.Equal(t, "b", got[0].Host)
	require.Equal(t, 2, rep.Checked[ServedRequestFinished].Judged, "the 502 and the answer cut short are not judged")
}

func TestReadEscrowTimeoutsPrefersSession(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "postgres"), 0o755))
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "postgres", name), []byte(body), 0o644))
	}
	write("public.devshard_sessions.csv", "epoch_id,escrow_id,config_json\n"+
		`1,2,"{""RefusalTimeout"":60,""ExecutionTimeout"":1920}"`+"\n")
	write("public.devshard_escrow_cache.csv", "escrow_id,epoch_id,escrow_json,cached_at\n"+
		`2,1,"{""config"":{""refusal_timeout"":1,""execution_timeout"":2}}",0`+"\n"+
		`3,1,"{""config"":{""refusal_timeout"":30,""execution_timeout"":90}}",0`+"\n")
	got, err := ReadEscrowTimeouts(dir)
	require.NoError(t, err)
	require.Equal(t, EscrowTimeouts{Refusal: 60, Execution: 1920}, got["2"], "the session config wins")
	require.Equal(t, EscrowTimeouts{Refusal: 30, Execution: 90}, got["3"], "the cache fills an escrow without a session")
}

func TestExportManifestSaysWhenTheExportWasTaken(t *testing.T) {
	e, dir := newExporter(t, ExportLimits{})
	_, err := e.WriteBytes("a.txt", []byte("a"))
	require.NoError(t, err)
	before := time.Now()
	m, err := e.Finish()
	require.NoError(t, err)
	require.False(t, m.ExportedAt.Before(before.Truncate(time.Second)))

	at, err := ExportTime(dir)
	require.NoError(t, err)
	require.True(t, at.Equal(m.StartedAt), "checks read the manifest's time, not the file's mtime")
}

// writeDeadlineRun is a checked run whose export names when it was taken and the escrow's deadlines.
func writeDeadlineRun(t *testing.T, exportedAt time.Time, finished bool) string {
	t.Helper()
	dir := writeCheckedRun(t, 64, &mockopenai.RequestRecord{Phase: mockopenai.PhaseExecute, Model: "model-a",
		Node: "mock-openai-3", Seed: intPtr(1), Status: 200, Outcome: "ok", TokensOut: 64, UsageCompletion: 64})
	sessions := filepath.Join(dir, "export", "db-postgres-versiond-3", "postgres", "public.devshard_sessions.csv")
	require.NoError(t, os.WriteFile(sessions, []byte("epoch_id,escrow_id,config_json,status\n"+
		`1,4,"{""RefusalTimeout"":30,""ExecutionTimeout"":90}",active`+"\n"), 0o644))
	if !finished {
		writeOpenStart(t, dir)
	}
	raw, err := json.Marshal(ExportManifest{ExportedAt: exportedAt, Complete: true})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "export", exportManifestName), raw, 0o644))
	return dir
}

// Test flow:
//  1. A run's executor started an inference and never closed it; the export was taken an hour later.
//  2. Check the run, and the same run with the export taken within the deadline.
//  3. Check the open inference is a violation naming the host, and within the deadline it is only
//     listed as open.
func TestCheckLoadtestRunCatchesAnInferenceLeftOpenPastItsDeadline(t *testing.T) {
	rep, err := CheckLoadtestRun(writeDeadlineRun(t, time.Unix(1_792_350_000, 0).Add(time.Hour), false), nil)
	require.NoError(t, err)
	got := violations(rep, InferenceClosesInTime)
	require.Len(t, got, 1, "%+v", rep)
	require.Equal(t, "postgres-versiond-3", got[0].Host)
	require.Contains(t, got[0].Detail, "still Started at export")

	rep, err = CheckLoadtestRun(writeDeadlineRun(t, time.Unix(1_792_350_000, 0).Add(time.Minute), false), nil)
	require.NoError(t, err)
	require.Empty(t, violations(rep, InferenceClosesInTime))
	require.Len(t, rep.Open, 1)

	rep, err = CheckLoadtestRun(writeDeadlineRun(t, time.Unix(1_792_350_000, 0).Add(time.Hour), true), nil)
	require.NoError(t, err)
	require.Empty(t, rep.Violations, "%+v", rep)
	require.True(t, rep.Checked[InferenceClosesInTime].Measured)
	require.True(t, rep.Checked[ServedRequestFinished].Measured)
}

// Test flow:
// 1. A run's export holds no manifest, so the time it was taken is unknown.
// 2. Check the run.
// 3. Check both deadline checks say they were not measured instead of passing.
func TestCheckLoadtestRunWithoutTheExportTimeDoesNotJudgeDeadlines(t *testing.T) {
	dir := writeCheckedRun(t, 64, nil)
	rep, err := CheckLoadtestRun(dir, nil)
	require.NoError(t, err)
	require.False(t, rep.Checked[InferenceClosesInTime].Measured)
	require.Contains(t, rep.Checked[InferenceClosesInTime].Note, "export time unknown")
	require.False(t, rep.Checked[ServedRequestFinished].Measured)
}

// writeOpenStart replaces the executor's chain with a Start and a ConfirmStart and no outcome: the
// Start at floor 100, a host's ack of 101 in its diff, the ConfirmStart in the next one, anchored at
// 101; the blocks are a second apart.
func writeOpenStart(t *testing.T, dir string) {
	t.Helper()
	const at = int64(1_792_350_000)
	diff := func(nonce uint64, txs ...*types.DevshardTx) string {
		raw, err := proto.Marshal(&types.DiffContent{Nonce: nonce, EscrowId: "4", Txs: txs})
		require.NoError(t, err)
		return `\x` + hex.EncodeToString(raw)
	}
	start := stampedStart(&types.MsgStartInference{InferenceId: 1, Model: "model-a", MaxTokens: 256}, 100)
	diffs := filepath.Join(dir, "export", "db-postgres-versiond-3", "postgres", "public.devshard_diffs.csv")
	require.NoError(t, os.WriteFile(diffs, []byte("escrow_id,nonce,txs_proto,post_state_root\n"+
		"4,1,"+diff(1, start, heightAck(3, 101))+",\\xaa\n4,2,"+diff(2, confirmStart(1, 100))+",\\xbb\n"), 0o644))
	writeChainHeaders(t, filepath.Join(dir, "export"), map[uint64]time.Time{100: time.Unix(at, 0), 101: time.Unix(at+1, 0)})
}

// Test flow:
//  1. Two hosts hold a served inference, finished on both, on an escrow with the chain's default
//     timeouts (60 s and 32 min); the export comes a minute after the start, within its deadline.
//  2. Check the run.
//  3. Check both deadline checks judge it: a Finish on the chain is final, so the inference left
//     Started in time on each host and the request has its Finish on every host, whatever the
//     deadline, and nothing is listed as open.
func TestPromiseDeadlineChecksJudgeWhatClosedBeforeTheExport(t *testing.T) {
	finished := func() *InferenceFacts {
		return &InferenceFacts{Started: true, StartedAt: t0.Unix(), Confirmed: true, ConfirmedAt: t0.Unix(), Finished: true}
	}
	in := PromiseInput{
		Diffs: ExportDiffs{Hosts: []HostChain{
			hostChain("a", map[uint64]*InferenceFacts{1: finished()}, nil),
			hostChain("b", map[uint64]*InferenceFacts{1: finished()}, nil),
		}},
		Requests:      []ClientRequest{served(1, 200)},
		Timeouts:      map[string]EscrowTimeouts{"2": {Refusal: 60, Execution: 1920}},
		ObservedUntil: t0.Add(time.Minute),
		Slack:         DeadlineSlack,
	}
	rep := CheckPromises(in)
	require.Empty(t, violations(rep, InferenceClosesInTime))
	require.Empty(t, violations(rep, ServedRequestFinished))
	require.Equal(t, 2, rep.Checked[InferenceClosesInTime].Judged, "%+v", rep.Checked[InferenceClosesInTime])
	require.Equal(t, 1, rep.Checked[ServedRequestFinished].Judged, "%+v", rep.Checked[ServedRequestFinished])
	require.Empty(t, rep.Open)
}

// Test flow:
//  1. Two hosts hold one escrow. Host a holds nonces 1-3: Start, ConfirmStart, Finish. Host b stopped
//     at nonce 2 with the same roots: the gateway sends each diff to one host, and nothing reached b
//     after the load.
//  2. Check the run an hour after the deadline.
//  3. Check host b, only behind, is reported neither as holding the inference open nor as missing
//     the Finish of the 200, and the report says it was behind.
func TestPromiseDeadlineDoesNotBlameAHostThatIsOnlyBehind(t *testing.T) {
	open := func() *InferenceFacts {
		return &InferenceFacts{Started: true, StartedAt: t0.Unix(), Confirmed: true, ConfirmedAt: t0.Unix() + 1}
	}
	finished := open()
	finished.Finished = true
	in := PromiseInput{
		Diffs: ExportDiffs{Hosts: []HostChain{
			hostChain("a", map[uint64]*InferenceFacts{1: finished}, map[uint64]string{1: "aa", 2: "bb", 3: "cc"}),
			hostChain("b", map[uint64]*InferenceFacts{1: open()}, map[uint64]string{1: "aa", 2: "bb"}),
		}},
		Requests:      []ClientRequest{served(1, 200)},
		Timeouts:      map[string]EscrowTimeouts{"2": {Refusal: 60, Execution: 60}},
		ObservedUntil: t0.Add(time.Hour),
	}
	rep := CheckPromises(in)
	require.Empty(t, violations(rep, InferenceClosesInTime), "host b's chain is a prefix of host a's: lag, not a stuck inference")
	require.Empty(t, violations(rep, ServedRequestFinished), "the Finish is on the chain; host b was not sent it yet")
	require.True(t, rep.Checked[InferenceClosesInTime].Measured)
	require.True(t, rep.Checked[ServedRequestFinished].Measured)
	require.Contains(t, rep.Observations, "host b behind the tip of escrow 2 by 1 diffs at the export; judged on the tip's chain")

	// A host whose roots diverge from the tip is not behind: it is judged on its own chain.
	in.Diffs.Hosts[1] = hostChain("b", map[uint64]*InferenceFacts{1: open()}, map[uint64]string{1: "aa", 2: "zz"})
	rep = CheckPromises(in)
	require.Len(t, violations(rep, InferenceClosesInTime), 1)
	require.Len(t, violations(rep, ServedRequestFinished), 1)
}

// Test flow:
//  1. Host a holds the Start (nonce 1) and the ConfirmStart (nonce 2); host b only the Start. Refusal
//     5 s, execution 90 s.
//  2. Check 250 s after the start: past the refusal deadline (5 + 215 s), within the execution one
//     (1 + 90 + 215 s).
//  3. Check the lagging host is not judged by the refusal deadline of a state the chain has left.
func TestPromiseDeadlineOfALaggingHostIsTheChains(t *testing.T) {
	in := PromiseInput{
		Diffs: ExportDiffs{Hosts: []HostChain{
			hostChain("a", map[uint64]*InferenceFacts{1: {Started: true, StartedAt: t0.Unix(), Confirmed: true, ConfirmedAt: t0.Unix() + 1}},
				map[uint64]string{1: "aa", 2: "bb"}),
			hostChain("b", map[uint64]*InferenceFacts{1: {Started: true, StartedAt: t0.Unix()}}, map[uint64]string{1: "aa"}),
		}},
		Timeouts:      map[string]EscrowTimeouts{"2": {Refusal: 5, Execution: 90}},
		ObservedUntil: t0.Add(250 * time.Second),
		Slack:         DeadlineSlack,
	}
	require.Empty(t, violations(CheckPromises(in), InferenceClosesInTime),
		"the inference is Started on the chain and within its execution deadline")
}

// Test flow:
//  1. An export holds no timeouts for the escrow; the export was taken a second after the start.
//  2. Check the served request.
//  3. Check a deadline that is unknown is not taken as passed.
func TestPromiseServedFinishWaitsWhenTheDeadlineIsUnknown(t *testing.T) {
	in := PromiseInput{
		Diffs: ExportDiffs{Hosts: []HostChain{
			hostChain("a", map[uint64]*InferenceFacts{1: {Started: true, StartedAt: t0.Unix(), Confirmed: true, ConfirmedAt: t0.Unix()}}, nil),
		}},
		Requests:      []ClientRequest{served(1, 200)},
		ObservedUntil: t0.Add(time.Second),
	}
	rep := CheckPromises(in)
	require.Empty(t, violations(rep, ServedRequestFinished), "an escrow whose timeouts are unknown cannot be called settled")
	require.False(t, rep.Checked[ServedRequestFinished].Measured)
	require.Contains(t, rep.Checked[ServedRequestFinished].Note, "without known timeouts")
}

// Test flow:
//  1. An inference sits on an escrow whose timeouts the export does not hold.
//  2. Check the deadline check.
//  3. Check the note says what it could not judge instead of leaving it out silently.
func TestPromiseDeadlineSaysWhatItCouldNotJudge(t *testing.T) {
	in := PromiseInput{
		Diffs: ExportDiffs{Hosts: []HostChain{
			hostChain("a", map[uint64]*InferenceFacts{1: {Started: true, StartedAt: t0.Unix(), Confirmed: true, ConfirmedAt: t0.Unix()}}, nil),
		}},
		ObservedUntil: t0.Add(time.Hour),
	}
	require.Contains(t, CheckPromises(in).Checked[InferenceClosesInTime].Note, "without known timeouts")
}

// Test flow:
//  1. A host keeps its chain on SQLite: its sessions table holds the escrow's config with timeouts, and
//     no Postgres database holds the escrow.
//  2. Read the export's timeouts.
//  3. Check the SQLite session supplies them, as it already supplies the vote threshold.
func TestReadEscrowTimeoutsReadsSQLiteSessions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sqlite", "versiond-0", "opt", "versiond", "data", "a.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	cfg, err := json.Marshal(types.SessionConfig{RefusalTimeout: 5, ExecutionTimeout: 30})
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE sessions (escrow_id TEXT, config_json TEXT, group_json TEXT, status TEXT);
		CREATE TABLE diffs (escrow_id TEXT, nonce INTEGER, txs_proto BLOB, post_state_root BLOB)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sessions VALUES ('7', ?, '[]', 'active')`, string(cfg))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	got, err := ReadEscrowTimeouts(dir)
	require.NoError(t, err)
	require.Equal(t, EscrowTimeouts{Refusal: 5, Execution: 30}, got["7"])
}

// Test flow:
//  1. One escrow's session row holds config_json that does not parse; another's is fine.
//  2. Read the timeouts.
//  3. Check the good escrow is read and the broken one is only left out.
func TestReadEscrowTimeoutsSkipsOneBrokenRow(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "postgres"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "postgres", "public.devshard_sessions.csv"), []byte(
		"epoch_id,escrow_id,config_json\n"+
			`1,2,"{""RefusalTimeout"":60,"`+"\n"+
			`1,3,"{""RefusalTimeout"":30,""ExecutionTimeout"":90}"`+"\n"), 0o644))
	got, err := ReadEscrowTimeouts(dir)
	require.NoError(t, err)
	require.Equal(t, EscrowTimeouts{Refusal: 30, Execution: 90}, got["3"])
	require.NotContains(t, got, "2")
}

// Test flow:
//  1. An export starts, writes a file, and finishes.
//  2. Read the export's time.
//  3. Check it is when the export started: the first database was read then, so a host is not judged
//     as if it had until the end of the export.
func TestExportTimeIsWhenTheExportStarted(t *testing.T) {
	e, dir := newExporter(t, ExportLimits{})
	started := time.Now().UTC()
	time.Sleep(1100 * time.Millisecond)
	_, err := e.WriteBytes("a.txt", []byte("a"))
	require.NoError(t, err)
	m, err := e.Finish()
	require.NoError(t, err)
	require.False(t, m.StartedAt.IsZero())
	require.True(t, m.StartedAt.Before(m.ExportedAt))

	at, err := ExportTime(dir)
	require.NoError(t, err)
	require.True(t, at.Equal(m.StartedAt), "deadlines are judged against the start of the export")
	require.WithinDuration(t, started, at, time.Second)
}

// Test flow:
//  1. Three hosts hold escrow 2. Inference 1 is finished on every host; inference 2 is finished on
//     host a and still Started on host b; inference 3 is Pending on host a only; inference 4 is a
//     burned nonce; host c is only behind on the escrow. Refusal 60 s, execution 1920 s.
//  2. Ask, a minute after the start, which inferences a later export could judge otherwise, then
//     again once their deadlines passed.
//  3. Check inferences 2 and 3 are named with their latest deadline on any host, the finished, the
//     burned and the lagging host's are not, and nothing is named past the deadlines.
func TestPromiseDeadlinesAheadNameWhatALaterExportCouldJudgeOtherwise(t *testing.T) {
	facts := func(confirmed, finished bool) *InferenceFacts {
		f := &InferenceFacts{Started: true, StartedAt: t0.Unix(), Finished: finished}
		if confirmed {
			f.ConfirmedAt, f.Confirmed = t0.Unix()+1, true
		}
		return f
	}
	ghost := &InferenceFacts{Started: true, StartedAt: t0.Unix(), PromptHash: ghostProbePromptHash}
	in := PromiseInput{
		Diffs: ExportDiffs{Hosts: []HostChain{
			hostChain("a", map[uint64]*InferenceFacts{1: facts(true, true), 2: facts(true, true), 3: facts(false, false), 4: ghost},
				map[uint64]string{1: "aa", 2: "bb", 3: "cc"}),
			hostChain("b", map[uint64]*InferenceFacts{1: facts(true, true), 2: facts(true, false)}, map[uint64]string{1: "aa", 2: "bb", 3: "cc"}),
			hostChain("c", map[uint64]*InferenceFacts{1: facts(true, false), 5: facts(true, false)}, map[uint64]string{1: "aa"}),
		}},
		Timeouts:      map[string]EscrowTimeouts{"2": {Refusal: 60, Execution: 1920}},
		ObservedUntil: t0.Add(time.Minute),
		Slack:         DeadlineSlack,
	}
	grace := TimeoutSkew + TimeoutVoteWindow + TimeoutSweepPeriod + DeadlineSlack
	require.Equal(t, []InferenceDue{
		{InferenceRef: ref(2), Due: t0.Add(time.Second + 1920*time.Second + grace)},
		{InferenceRef: ref(3), Due: t0.Add(60*time.Second + grace)},
	}, DeadlinesAhead(in))

	in.ObservedUntil = t0.Add(time.Hour)
	require.Empty(t, DeadlinesAhead(in))
}
