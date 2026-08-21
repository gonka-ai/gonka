package heightsync

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCloseReady_ArmsAfterIdle(t *testing.T) {
	cfg := DefaultHeartbeatConfig()
	now := time.Unix(1_700_000_000, 0)
	c := NewCloseReady(1, cfg)
	c.SetClock(func() time.Time { return now })

	c.NoteContact(100, 100)
	now = now.Add(cfg.IdleTimeout)
	armed, _ := c.Armed()
	require.False(t, armed, "exactly T_idle has not passed T_idle")

	now = now.Add(time.Millisecond)
	armed, at := c.Armed()
	require.True(t, armed, "silence past T_idle arms")
	require.Equal(t, uint64(100), at,
		"a silent host learns no new height, so it can only cite the last one it saw")

	ev := c.TimeoutEvidence()
	require.Equal(t, uint32(1), ev.Slot)
	require.Equal(t, uint64(100), ev.LastSignalHeight)
	require.Equal(t, uint64(100), ev.LastUserHeightClaim)
	require.Equal(t, uint64(100), ev.ArmedAtHeight)
	require.Equal(t, cfg.IdleTimeout+time.Millisecond, ev.SilentFor)
}

func TestCloseReady_ArmsWithoutAnyTick(t *testing.T) {
	// Arming is level-triggered on read: a host with no ticker and no inbound
	// traffic still notices the silence.
	cfg := DefaultHeartbeatConfig()
	now := time.Unix(1_700_000_000, 0)
	c := NewCloseReady(0, cfg)
	c.SetClock(func() time.Time { return now })

	c.NoteContact(100, 100)
	now = now.Add(cfg.IdleTimeout + time.Second)
	armed, _ := c.Armed()
	require.True(t, armed)
	require.Equal(t, cfg.IdleTimeout+time.Second, c.SilentFor())
}

func TestCloseReady_DisarmsOnContact(t *testing.T) {
	cfg := DefaultHeartbeatConfig()
	start := time.Unix(1_700_000_000, 0)
	now := start
	c := NewCloseReady(0, cfg)
	c.SetClock(func() time.Time { return now })

	c.NoteContact(100, 100)
	now = now.Add(cfg.IdleTimeout + time.Second)
	armed, _ := c.Armed()
	require.True(t, armed)

	now = now.Add(time.Second)
	c.NoteContact(105, 105)
	armed, _ = c.Armed()
	require.False(t, armed, "contact disarms")

	ivs := c.Intervals()
	require.Len(t, ivs, 1)
	require.Equal(t, uint64(100), ivs[0].ArmedAt)
	require.Equal(t, uint64(105), ivs[0].DisarmedAt)
	require.Equal(t, now.Sub(start), ivs[0].SilentFor)
}

func TestCloseReady_MissingAckDoesNotArm(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := NewCloseReady(2, DefaultHeartbeatConfig())
	c.SetClock(func() time.Time { return now })

	c.NoteContact(100, 99)
	c.SetTurnContext(1, []uint64{1})
	armed, _ := c.Armed()
	require.False(t, armed, "a degraded turn is context, never a reason to arm")
	require.Equal(t, []uint64{1}, c.TimeoutEvidence().DegradedTurns)
}

func TestCloseReady_NoContactNeverArms(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := NewCloseReady(0, DefaultHeartbeatConfig())
	c.SetClock(func() time.Time { return now })

	now = now.Add(time.Hour)
	armed, _ := c.Armed()
	require.False(t, armed)
	require.Zero(t, c.SilentFor())
}

func TestCloseReady_IdleTimeoutSurvivesOneLostTurnover(t *testing.T) {
	cfg := DefaultHeartbeatConfig()
	now := time.Unix(1_700_000_000, 0)
	c := NewCloseReady(0, cfg)
	c.SetClock(func() time.Time { return now })

	c.NoteContact(100, 100)
	now = now.Add(cfg.Interval + cfg.TurnTimeout)
	armed, _ := c.Armed()
	require.False(t, armed, "one lost turnover must not arm a host")
}
