package harness

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPromiseReportSaysWhatEachCheckPromisesAndWhereItBroke(t *testing.T) {
	rep := CheckPromises(PromiseInput{})
	rep.Checked[PaidOutputWithinProduced] = Measure{Measured: true, Judged: 12, Note: "12 finishes | judged"}
	for i := 0; i < promiseExamples+2; i++ {
		rep.Violations = append(rep.Violations, PromiseViolation{Promise: PaidOutputWithinProduced, Host: "versiond-3",
			Inference: &InferenceRef{Escrow: "4", Inference: uint64(i + 1)}, Detail: "Finish output_tokens 199, the ML produced [64]"})
	}
	var b strings.Builder
	require.NoError(t, WritePromiseReport(&b, "System promise checks: dishonest-executor-overclaim", rep))
	out := b.String()

	require.Contains(t, out, "1 checks violated, 0 held, 11 without a verdict")
	require.Contains(t, out, "| paid_output_within_produced | A paid Finish.OutputTokens equals the tokens the ML execution produced and never exceeds max_tokens. "+
		"A Finish whose inference was invalidated paid nothing: its overclaim is caught and counted apart | VIOLATED | 7 | 12 finishes \\| judged |",
		"a pipe in a note does not break the table")
	require.Contains(t, out, "| charge_only_the_guilty | ")
	require.Contains(t, out, "### paid_output_within_produced — 7")
	require.Contains(t, out, "- `host versiond-3, escrow 4 inference 1`: Finish output_tokens 199, the ML produced [64]")
	require.Contains(t, out, "- … 2 more in the JSON report")
	for _, c := range PromiseCatalog {
		require.Contains(t, out, "| "+c.Name+" |", "every check has a row")
	}
}

func TestPromiseReportShowsTheChallengeWave(t *testing.T) {
	rep := CheckPromises(PromiseInput{})
	rep.Wave = &ChallengeWave{Outcomes: map[string]int{"invalidated": 7, "none": 3}, Reasons: map[string]int{"empty_stream": 10},
		Votes: 12, Rejected: map[string]int{"late": 4}, NotAwaited: 2, VoteFailed: 2, VoteBusy: 1,
		TriggerToVerdictMs: []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120, 130, 140, 150, 160, 170, 180, 190, 200, 900}}
	var b strings.Builder
	require.NoError(t, WritePromiseReport(&b, "System promise checks: usage-challenge-wave", rep))
	out := b.String()
	require.Contains(t, out, "| invalidated | 7 |\n| none | 3 |")
	require.Contains(t, out, "| empty_stream | 10 |")
	require.Contains(t, out, "20 voter answers: 12 votes, 2 not awaited, 2 failed (1 still busy with 429 or 503 at the deadline), 4 late (20.0%).")
	require.Contains(t, out, "| late | 4 |")
	require.Contains(t, out, "From the challenge's start to its outcome, 21 challenges: p50 110 ms, p95 200 ms, max 900 ms.")
}

func TestPromiseSummaryHasARowPerRunAndAColumnPerCheck(t *testing.T) {
	violated := CheckPromises(PromiseInput{})
	violated.Checked[NodeLocksReleased] = Measure{Measured: true, Judged: 1}
	violated.Violations = []PromiseViolation{{Promise: NodeLocksReleased}, {Promise: NodeLocksReleased}}
	clean := CheckPromises(PromiseInput{})
	clean.Checked[NodeLocksReleased] = Measure{Measured: true, Judged: 1}
	clean.Checked[OneNoncePerRequest] = Measure{Note: "not measured: no allowance: 3 burned nonces"}

	var b strings.Builder
	require.NoError(t, WritePromiseSummary(&b, []NamedPromiseReport{{"vote-fanout", violated}, {"catch-up", clean}}))
	out := b.String()
	lines := strings.Split(out, "\n")
	row := func(name string) string {
		for _, l := range lines {
			if strings.HasPrefix(l, "| "+name+" |") {
				return l
			}
		}
		return ""
	}
	require.Equal(t, len(PromiseCatalog)+2, strings.Count(row("vote-fanout"), "|"))
	require.Contains(t, row("vote-fanout"), " **2** |")
	require.Contains(t, row("catch-up"), " ok | obs |", "locks held, nonce burns counted without an allowance")
	require.Less(t, strings.Index(out, "| catch-up |"), strings.Index(out, "| vote-fanout |"), "runs in name order")
}

// Test flow:
//  1. A saved report of a run that is not valid, with a check that held.
//  2. Write the summary.
//  3. Check the row says the run is not valid and shows no "ok".
func TestPromiseSummaryMarksARunThatIsNotValid(t *testing.T) {
	rep := CheckPromises(PromiseInput{})
	rep.Checked[NodeLocksReleased] = Measure{Measured: true, Judged: 1}
	rep.Invalid = []string{"the generator dropped 1 of 2 requests at max_in_flight"}
	var b strings.Builder
	require.NoError(t, WritePromiseSummary(&b, []NamedPromiseReport{{"dropped", rep}}))
	row := ""
	for _, l := range strings.Split(b.String(), "\n") {
		if strings.HasPrefix(l, "| dropped") {
			row = l
		}
	}
	require.NotEmpty(t, row)
	require.Contains(t, row, "not valid")
	require.NotContains(t, row, " ok |", "a run that is not valid judged nothing")
}

// Test flow:
//  1. Two runs under one name and a run whose name holds a pipe.
//  2. Write the summary.
//  3. Check the caller's slice keeps its order, runs of one name keep theirs, and the pipe does not
//     split the row.
func TestPromiseSummaryKeepsTheCallersSliceAndEscapesNames(t *testing.T) {
	first, second := CheckPromises(PromiseInput{}), CheckPromises(PromiseInput{})
	second.Checked[NodeLocksReleased] = Measure{Measured: true, Judged: 1}
	runs := []NamedPromiseReport{{"z", first}, {"a|b", first}, {"same", first}, {"same", second}}
	var b strings.Builder
	require.NoError(t, WritePromiseSummary(&b, runs))
	require.Equal(t, "z", runs[0].Name, "the caller's slice is not sorted in place")
	out := b.String()
	require.Contains(t, out, "| a\\|b |")
	firstSame := strings.Index(out, "| same | —")
	require.GreaterOrEqual(t, firstSame, 0)
	require.Contains(t, out[firstSame+1:], "| same |", "runs of one name keep their order")
}
