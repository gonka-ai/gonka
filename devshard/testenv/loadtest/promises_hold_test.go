package loadtest

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/types"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// Test flow:
//  1. The Gateway holds two open inferences, due one and three minutes from now; the hold may last
//     ten minutes.
//  2. Decide the hold now, two and four minutes later, with nothing open, and next to and at its limit.
//  3. Check it waits while an open inference is within its deadline, names the next and the last
//     deadline, wakes after the poll or at the limit, whichever comes first, and stops when nothing is
//     open, when every open inference is past its deadline, and at the limit.
func TestPromisesHoldWaitsWhileAnOpenInferenceIsWithinItsDeadline(t *testing.T) {
	now := time.Unix(1_792_350_000, 0)
	limit := now.Add(10 * time.Minute)
	poll := 5 * time.Second
	open := []openInference{
		{Escrow: "1", ID: "7", Due: now.Add(time.Minute)},
		{Escrow: "2", ID: "9", Due: now.Add(3 * time.Minute)},
	}

	step := decideHold(open, now, limit, poll)
	require.True(t, step.Wait)
	require.Equal(t, 2, step.Open)
	require.Equal(t, now.Add(time.Minute), step.NextDue)
	require.Equal(t, now.Add(3*time.Minute), step.LastDue)
	require.Equal(t, now.Add(poll), step.Wake)

	step = decideHold(open, now.Add(2*time.Minute), limit, poll)
	require.True(t, step.Wait)
	require.Equal(t, 1, step.Open, "the first is past its deadline: the checks judge it as it stands")
	require.Equal(t, now.Add(3*time.Minute), step.NextDue)

	step = decideHold(open, now.Add(4*time.Minute), limit, poll)
	require.False(t, step.Wait)
	require.Equal(t, "every open inference past its deadline", step.Reason)

	step = decideHold(nil, now, limit, poll)
	require.False(t, step.Wait)
	require.Equal(t, "no inference open", step.Reason)

	late := []openInference{{Escrow: "1", ID: "7", Due: limit.Add(time.Minute)}}
	step = decideHold(late, limit.Add(-2*time.Second), limit, poll)
	require.True(t, step.Wait)
	require.Equal(t, limit, step.Wake)
	step = decideHold(late, limit, limit, poll)
	require.False(t, step.Wait)
	require.Equal(t, "limit reached", step.Reason)
}

// Test flow:
//  1. One read of the Gateway: escrow 1 holds a Pending, a Started, a finished, a timed-out
//     inference, a burned nonce and a served warm-up probe; refusal 5 s, execution 90 s.
//  2. Pick the open inferences.
//  3. Check only the Pending and the Started ones are open, each due when the deadline checks call it
//     late: its timeout from its start or its confirmation, plus the Gateway's grace, one sweep and
//     the checks' slack.
func TestPromisesHoldPicksTheOpenInferencesWithTheChecksDeadlines(t *testing.T) {
	at := int64(1_792_350_000)
	snapshot := orphanSnapshot{Runtimes: []orphanRuntime{{EscrowID: "1", Active: true, Inferences: map[string]debugInference{
		"1": {Status: "pending", StartedAt: at},
		"2": {Status: "started", StartedAt: at, ConfirmedAt: at + 1},
		"3": {Status: "finished", StartedAt: at, ConfirmedAt: at},
		"4": {Status: "timed_out", StartedAt: at},
		"5": {Status: "pending", StartedAt: at},
		"6": {Status: "started", StartedAt: at, ConfirmedAt: at},
	}}}}
	judge := orphanJudge{Ghosts: ghostSet{"": {"5": false}, "1": {"5": false}}, WarmUps: newEscrowNonces([]warmUpSlot{{EscrowID: "1", Nonce: "6"}})}
	timeouts := map[string]harness.EscrowTimeouts{"1": {Refusal: 5, Execution: 90}}

	open := openInferences(snapshot, timeouts, judge)
	grace := harness.TimeoutSkew + harness.TimeoutVoteWindow + harness.TimeoutSweepPeriod + harness.DeadlineSlack
	require.ElementsMatch(t, []openInference{
		{Escrow: "1", ID: "1", Due: time.Unix(at, 0).Add(5*time.Second + grace)},
		{Escrow: "1", ID: "2", Due: time.Unix(at+1, 0).Add(90*time.Second + grace)},
	}, open)
}

// holdGateway is a Gateway for the hold: escrow 1 is live, escrow 2 settled, each answering its
// scoped debug and state routes with refusal 5 s and execution 90 s.
type holdGateway struct {
	reads    atomic.Int32
	live     func(read int32) string // escrow 1's inferences at a read
	settled  string                  // escrow 2's inferences
	timeouts string
}

func (g *holdGateway) server(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/admin/devshards", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"devshards":[{"id":"1","active":true},{"id":"2","active":false}]}`)
	})
	mux.HandleFunc("/devshard/1/v1/debug/inferences", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, g.live(g.reads.Add(1)))
	})
	mux.HandleFunc("/devshard/2/v1/debug/inferences", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, g.settled)
	})
	for _, id := range []string{"1", "2"} {
		mux.HandleFunc("/devshard/"+id+"/v1/state", func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "Bearer admin-key", r.Header.Get("Authorization"))
			_, _ = fmt.Fprint(w, `{"session":{"config":{"refusal_timeout":5,"execution_timeout":90}}}`)
		})
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func inferencesBody(t *testing.T, inferences map[string]debugInference) string {
	raw, err := json.Marshal(debugInferencesResponse{Inferences: inferences})
	require.NoError(t, err)
	return string(raw)
}

// Test flow:
//  1. A run of four escrows held to the promises: escrow 1 holds one inference Started at the end of
//     the load, finished at the Gateway's third read, and a burned nonce left Pending; escrow 2
//     settled an hour ago with an inference still Started.
//  2. Hold the export, reading every 20 ms.
//  3. Check the hold ends once the Started inference finished, long before the fixed hold of the
//     scenario, without waiting on the burned nonce or on the settled escrow's inference, which is
//     past its deadline, while the hosts' chains hold every inference finished.
func TestPromisesHoldEndsWhenTheGatewayHoldsNothingOpen(t *testing.T) {
	now := time.Now().Unix()
	g := &holdGateway{
		live: func(read int32) string {
			status := "started"
			if read >= 3 {
				status = "finished"
			}
			return inferencesBody(t, map[string]debugInference{
				"1": {Status: status, StartedAt: now, ConfirmedAt: now},
				"2": {Status: "pending", StartedAt: now},
			})
		},
		settled: inferencesBody(t, map[string]debugInference{"1": {Status: "started", StartedAt: now - 3600, ConfirmedAt: now - 3600}}),
	}
	server := g.server(t)
	scenario := testScenario()
	scenario.Assertions.Promises = true
	scenario.Protocol = Protocol{RefusalTimeout: "5s", ExecutionTimeout: "90s"}
	scenario.Topology.Chain.Escrows = 4
	judge := orphanJudge{Ghosts: ghostSet{"": {"2": false}, "1": {"2": false}}}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	began := time.Now()
	require.NoError(t, holdForPromises(ctx, scenario, time.Now(), server.URL, "admin-key", judge, closedChains, 20*time.Millisecond, 20*time.Millisecond))
	require.Less(t, time.Since(began), 10*time.Second, "the fixed hold would last %s", scenario.PromisesWait())
	require.EqualValues(t, 3, g.reads.Load())
}

// Test flow:
//  1. A scenario without protocol timeouts runs on the chain's defaults, 60 s and 32 min; the
//     Gateway holds one inference Started that never closes, and the hold's limit, the end of the
//     load plus the longest hold, is 200 ms away.
//  2. Hold the export.
//  3. Check the limit ends the hold, and the limit is the hold under the chain's default timeouts.
func TestPromisesHoldStopsAtItsLimit(t *testing.T) {
	now := time.Now().Unix()
	g := &holdGateway{
		live: func(int32) string {
			return inferencesBody(t, map[string]debugInference{"1": {Status: "started", StartedAt: now, ConfirmedAt: now}})
		},
		settled: inferencesBody(t, nil),
	}
	server := g.server(t)
	scenario := testScenario()
	scenario.Assertions.Promises = true
	scenario.Topology.Chain.Escrows = 4
	require.Equal(t, promisesHold(60, 32*60), scenario.PromisesWait())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	began := time.Now()
	workloadEnd := began.Add(200*time.Millisecond - scenario.PromisesWait())
	require.NoError(t, holdForPromises(ctx, scenario, workloadEnd, server.URL, "admin-key", orphanJudge{}, closedChains, 20*time.Millisecond, 20*time.Millisecond))
	require.Less(t, time.Since(began), 10*time.Second)
	require.GreaterOrEqual(t, time.Since(began), 200*time.Millisecond)
}

// closedChains is a read of hosts' chains on which every inference is finished.
func closedChains(context.Context) (harness.PromiseInput, error) {
	return harness.PromiseInput{ObservedUntil: time.Now(), Slack: harness.DeadlineSlack}, nil
}

// Test flow:
//  1. The Gateway holds nothing open, but on the hosts' chains an inference is Finished on host a and
//     still Started on host b, its deadline on b two seconds away (execution 90 s).
//  2. Hold the export, reading every 20 ms, and check the run on the chains as they stand at its end.
//  3. Check the hold lasts until that deadline, as the fixed hold would, and the deadline check then
//     reports host b holding the inference open: closed on the Gateway is not closed on the chains.
func TestPromisesHoldWaitsForAnInferenceStillOpenOnAHostsChain(t *testing.T) {
	g := &holdGateway{live: func(int32) string { return inferencesBody(t, nil) }, settled: inferencesBody(t, nil)}
	server := g.server(t)
	scenario := testScenario()
	scenario.Assertions.Promises = true
	scenario.Protocol = Protocol{RefusalTimeout: "5s", ExecutionTimeout: "90s"}
	scenario.Topology.Chain.Escrows = 4

	grace := harness.TimeoutSkew + harness.TimeoutVoteWindow + harness.TimeoutSweepPeriod + harness.DeadlineSlack
	due := time.Now().Add(2 * time.Second).Truncate(time.Second)
	confirmed := due.Add(-90*time.Second - grace).Unix()
	ref := harness.InferenceRef{Escrow: "1", Inference: 7}
	host := func(name string, finished bool) harness.HostChain {
		return harness.HostChain{Host: name, Roots: map[string]map[uint64]string{"1": {1: "aa", 2: "bb"}},
			Inferences: map[harness.InferenceRef]*harness.InferenceFacts{ref: {Started: true, StartedAt: confirmed, ConfirmedAt: confirmed, Finished: finished}}}
	}
	reads := 0
	chains := func(context.Context) (harness.PromiseInput, error) {
		reads++
		return harness.PromiseInput{
			Diffs:         harness.ExportDiffs{Hosts: []harness.HostChain{host("a", true), host("b", false)}},
			Timeouts:      map[string]harness.EscrowTimeouts{"1": {Refusal: 5, Execution: 90}},
			ObservedUntil: time.Now(), Slack: harness.DeadlineSlack,
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, holdForPromises(ctx, scenario, time.Now(), server.URL, "admin-key", orphanJudge{}, chains, 20*time.Millisecond, 20*time.Millisecond))
	ended := time.Now()
	require.True(t, ended.After(due), "the hold ended %s before the deadline on host b", due.Sub(ended))
	require.Greater(t, reads, 1)

	in, err := chains(ctx)
	require.NoError(t, err)
	in.ObservedUntil = ended
	var late []harness.PromiseViolation
	for _, v := range harness.CheckPromises(in).Violations {
		if v.Promise == harness.InferenceClosesInTime {
			late = append(late, v)
		}
	}
	require.Len(t, late, 1, "%+v", late)
	require.Equal(t, "b", late[0].Host)
}

// Test flow:
//  1. A host's Postgres holds escrow 4's session (refusal 5 s, execution 30 s) and the diffs Start
//     and ConfirmStart of inference 1; a second read finds the Finish too, and the database sends
//     nonce 2 again.
//  2. Read the chains with the probe twice.
//  3. Check the first read asks for every diff and sees the inference open, the second asks only for
//     the diffs past nonce 2, keeps nonce 2 once and sees the inference finished, both with the
//     session's timeouts, laid out as the export lays them.
func TestChainProbeReadsTheHostsChainsAsTheExportDoes(t *testing.T) {
	diff := func(nonce uint64, tx *types.DevshardTx) string {
		raw, err := proto.Marshal(&types.DiffContent{Nonce: nonce, EscrowId: "4", Txs: []*types.DevshardTx{tx}})
		require.NoError(t, err)
		return fmt.Sprintf("4,%d,\\x%s,\\x%02x\n", nonce, hex.EncodeToString(raw), nonce)
	}
	start := diff(1, &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: 1, StartedAt: 1_792_350_000}}})
	confirm := diff(2, &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ConfirmedAt: 1_792_350_001}}})
	finish := diff(3, &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 1, OutputTokens: 8}}})
	const header = "escrow_id,nonce,txs_proto,post_state_root\n"
	var asked []string
	stack := stackAccess{
		psql: func(service, database string) harness.PSQL {
			return func(sql string, _ int64) (string, error) {
				switch {
				case strings.HasPrefix(sql, "SELECT c.relname"):
					return "devshard_diffs\ndevshard_sessions\n", nil
				case strings.Contains(sql, "public.devshard_sessions"):
					return "epoch_id,escrow_id,config_json,status\n" + `1,4,"{""RefusalTimeout"":5,""ExecutionTimeout"":30}",active` + "\n", nil
				case strings.Contains(sql, "public.devshard_diffs"):
					asked = append(asked, sql)
					if len(asked) == 1 {
						return header + start + confirm, nil
					}
					return header + confirm + finish, nil
				}
				return "", fmt.Errorf("unexpected statement %q", sql)
			}
		},
		storageMode: func(string) (string, error) { return "postgres", nil },
	}
	cfg := &config.File{Hosts: []config.HostCfg{{ID: "versiond-0"}}}
	cfg.Postgres.Enabled, cfg.Postgres.PerParticipant, cfg.Postgres.Host = true, false, "postgres"
	probe := &chainProbe{cfg: cfg, stack: stack, dir: t.TempDir()}

	in, err := probe.read(context.Background())
	require.NoError(t, err)
	require.NotContains(t, asked[0], "VALUES")
	ref := harness.InferenceRef{Escrow: "4", Inference: 1}
	require.Len(t, in.Diffs.Hosts, 1)
	require.False(t, in.Diffs.Hosts[0].Inferences[ref].Finished)
	require.Equal(t, harness.EscrowTimeouts{Refusal: 5, Execution: 30}, in.Timeouts["4"])
	require.False(t, in.ObservedUntil.IsZero())

	in, err = probe.read(context.Background())
	require.NoError(t, err)
	require.Contains(t, asked[1], "VALUES ('4', 2)")
	require.True(t, in.Diffs.Hosts[0].Inferences[ref].Finished)
	require.Equal(t, 3, in.Diffs.Hosts[0].Diffs, "nonce 2 sent twice is kept once")
}
