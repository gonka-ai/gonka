package escrow

import (
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
)

// Test flow:
//  1. Observe height 1000 at noon, then 1050 a minute later, then 1100 ten minutes after noon, then a height below the first.
//  2. Assert the first two measure nothing, the third measures six seconds a block, and the lower height starts the measurement over.
func TestTheBlockPaceMeasuresOnlyOverAHundredBlocks(t *testing.T) {
	noon := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	var pace blockPace

	if _, measured := pace.observe(1000, noon); measured {
		t.Fatal("observe(first height) measured, want nothing yet")
	}
	if _, measured := pace.observe(1050, noon.Add(time.Minute)); measured {
		t.Fatal("observe(50 blocks on) measured, want nothing under 100 blocks")
	}
	if blockTime, measured := pace.observe(1100, noon.Add(10*time.Minute)); !measured || blockTime != 6*time.Second {
		t.Fatalf("observe(100 blocks in 10 minutes) = %s, %v, want 6s, true", blockTime, measured)
	}
	if _, measured := pace.observe(900, noon.Add(11*time.Minute)); measured {
		t.Fatal("observe(a lower height) measured, want the measurement to start over")
	}
}

// Test flow:
//  1. Build a manager with a 60-block margin over a chain measured at six seconds a block (six minutes of margin, under two 11-minute settle windows).
//  2. Check the margin on three ticks, then widen the margin to 600 blocks and check again, then narrow it back.
//  3. Assert the short margin was narrated once, not narrated at 600 blocks, and narrated again once it was short again.
func TestAShortSettleMarginIsNarratedOncePerEpisode(t *testing.T) {
	noon := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	now := noon
	cfg := config.Defaults()
	cfg.Rotation.SettleMarginBlocks = 60
	holder := config.NewHolder(&cfg)
	narrator := &recordingLifecycleNarrator{}
	manager := &Manager{config: holder, chainFacts: &fakeTxClient{}, narrator: narrator, now: func() time.Time { return now }}
	height := int64(1000)
	tick := func() {
		manager.checkSettleMargin(chain.PhaseSnapshot{EpochIndex: 8, BlockHeight: height})
		height += 100
		now = now.Add(10 * time.Minute)
	}

	tick()
	tick()
	tick()
	tick()
	wider := cfg
	wider.Rotation.SettleMarginBlocks = 600
	holder.Swap(&wider)
	tick()
	holder.Swap(&cfg)
	tick()

	want := "settle margin short 60 blocks of 6s, need 22m0s"
	if lines := slices.DeleteFunc(narrator.recorded(), func(line string) bool { return line != want }); len(lines) != 2 {
		t.Fatalf("narration = %v, want %q twice: once per episode", narrator.recorded(), want)
	}
}
