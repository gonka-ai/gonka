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

	require.Contains(t, out, "1 checks violated, 0 held, 1 without a verdict")
	require.Contains(t, out, "| paid_output_within_produced | Finish.OutputTokens equals the tokens the ML execution produced and never exceeds max_tokens | VIOLATED | 7 | 12 finishes \\| judged |",
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
