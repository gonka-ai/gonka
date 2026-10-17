package loadtest

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Write a compose log with one text-form and one JSON-form ghost_probe_skipped line, and an unrelated line.
// 2. Require both burns with their time, escrow, nonce, host label and reason, in log order.
func TestReadGatewayBurnsReadsTextAndJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.log")
	log := "devshardctl-1  | time=2026-10-15T14:35:00.5Z request=r1 stage=ghost_probe_skipped escrow=1 nonce=9 host=hukvxfc7 reason=no_compatible_request_after_stale\n" +
		`devshardctl-1  | {"time":"2026-10-15T14:35:01.04529705Z","level":"INFO","msg":"ghost_probe_skipped","stage":"ghost_probe_skipped","escrow":"8","nonce":833,"host":"kmyczv6w","kind":2,"reason":"participant_ejected_no_send"}` + "\n" +
		`devshardctl-1  | {"time":"2026-10-15T14:35:02Z","msg":"winner_selected","escrow":"8","nonce":834}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(log), 0o644))

	burns, err := readGatewayBurns(path)

	require.NoError(t, err)
	require.Equal(t, []gatewayBurn{
		{At: time.Date(2026, 10, 15, 14, 35, 0, 500000000, time.UTC), EscrowID: "1", Nonce: "9", Host: "hukvxfc7", Reason: "no_compatible_request_after_stale"},
		{At: time.Date(2026, 10, 15, 14, 35, 1, 45297050, time.UTC), EscrowID: "8", Nonce: "833", Host: "kmyczv6w", Reason: "participant_ejected_no_send"},
	}, burns)
}

// Test flow:
// 1. Lay out p2's fault window 3m-4m (graced to 5m35s) and an active escrow with 10 finished records and 6 burned nonces.
// 2. Burn 3 nonces of p2 as participant_ejected_no_send or participant_cut_off_no_send inside the graced window, 1 the same way after it, 1 inside it as participant_throttled_no_send, and 1 of honest p1 inside p2's window.
// 3. Require the 3 ejected or cut-off burns inside the window to be left out of the ghost rate and reported as excluded, and the other 3 to count.
// 4. Require the run to fail at a 20 % limit and pass at 25 %.
func TestGhostRateLeavesOutEjectedBurnsInsideFaultWindows(t *testing.T) {
	in := passingInputs()
	burns := []gatewayBurn{
		{At: at(3*time.Minute + 10*time.Second), EscrowID: "11", Nonce: "11", Host: "p2", Reason: "participant_ejected_no_send"},
		{At: at(3*time.Minute + 50*time.Second), EscrowID: "11", Nonce: "12", Host: "p2", Reason: "participant_cut_off_no_send"},
		{At: at(5 * time.Minute), EscrowID: "11", Nonce: "13", Host: "p2", Reason: "participant_ejected_no_send"},
		{At: at(6 * time.Minute), EscrowID: "11", Nonce: "14", Host: "p2", Reason: "participant_ejected_no_send"},
		{At: at(3*time.Minute + 20*time.Second), EscrowID: "11", Nonce: "15", Host: "p2", Reason: "participant_throttled_no_send"},
		{At: at(3*time.Minute + 30*time.Second), EscrowID: "11", Nonce: "16", Host: "p1", Reason: "participant_ejected_no_send"},
	}
	inferences := map[string]debugInference{}
	for nonce := 1; nonce <= 16; nonce++ {
		inferences[strconv.Itoa(nonce)] = debugInference{Status: "finished"}
	}
	poll := orphanPoll{Last: &orphanSnapshot{At: at(11 * time.Minute), Runtimes: []orphanRuntime{{EscrowID: "11", Active: true, Inferences: inferences}}}}
	ghosts := newGhostSet(burns, burnExcuse(in))

	summary, done, err := judgeOrphans(poll, orphanJudge{MaxGhostRate: 0.20, Ghosts: ghosts})

	require.True(t, done)
	require.Equal(t, 3, summary.GhostExcluded)
	require.Equal(t, 3, summary.Ghost)
	require.Equal(t, 13, summary.Total)
	require.InDelta(t, 3.0/13.0, summary.GhostRate, 1e-9)
	require.ErrorContains(t, err, "ghost inference rate 0.2308")

	_, done, err = judgeOrphans(poll, orphanJudge{MaxGhostRate: 0.25, Ghosts: ghosts})

	require.True(t, done)
	require.NoError(t, err)
}

// Test flow:
// 1. Burn nonce 7 on escrow 1 only.
// 2. Require escrow 1 to see it, escrow 2 not to, and a runtime read without its escrow to see every burn.
func TestGhostSetKeepsBurnsToTheirEscrow(t *testing.T) {
	ghosts := newGhostSet([]gatewayBurn{{EscrowID: "1", Nonce: "7"}}, nil)

	require.Equal(t, map[string]bool{"7": false}, ghosts.escrow("1"))
	require.Empty(t, ghosts.escrow("2"))
	require.Equal(t, map[string]bool{"7": false}, ghosts.escrow(""))
}
