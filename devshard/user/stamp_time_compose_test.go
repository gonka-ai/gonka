package user

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks/headertime"
	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/types"
)

func TestResolveStampTimeLocked_MapHitMissDisagree(t *testing.T) {
	session, _, _, _ := setupArmedSession(t, 3, 100_000, 10)
	hash := []byte{0xaa}
	src := headertime.New()
	require.NoError(t, src.Seed(100, hash, 5_000))
	session.sm.SetHeaderTimes(src)

	session.mu.Lock()
	defer session.mu.Unlock()

	ms, err := session.resolveStampTimeLocked(100, hash, 0)
	require.NoError(t, err)
	require.Equal(t, int64(5_000), ms)

	session.sm.SetHeaderTimes(headertime.New())
	ms, err = session.resolveStampTimeLocked(100, hash, 7_000)
	require.NoError(t, err)
	require.Equal(t, int64(7_000), ms, "map miss returns the optimistic candidate")

	session.sm.SetHeaderTimes(src)
	_, err = session.resolveStampTimeLocked(100, hash, 9_000)
	require.ErrorIs(t, err, heightsync.ErrStampTimeMismatch)
}

func TestStartCompose_FillsObservedTimeFromMap(t *testing.T) {
	session, _, _, rig := setupArmedSession(t, 3, 100_000, 10)
	floor, hash, known := session.sm.HeightSyncFloorAsOf(session.Nonce() + 1)
	require.True(t, known)
	require.NotZero(t, floor)
	require.True(t, heightsync.StampPresent(hash))

	row, ok := rig.gateway.StoredAt(floor)
	require.True(t, ok, "armed session poller stores the floor header time")
	require.Positive(t, row.TimeMs)

	prepared, err := session.PrepareInference(context.Background(), InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	require.NotNil(t, prepared)

	var start *types.MsgStartInference
	for _, tx := range prepared.diff.Txs {
		if s := tx.GetStartInference(); s != nil {
			start = s
			break
		}
	}
	require.NotNil(t, start)
	require.Equal(t, floor, start.ObservedHeight)
	require.Equal(t, row.TimeMs, start.ObservedTimeMs)
}

func TestStartCompose_EmptyMapUsesFloorTime(t *testing.T) {
	session, _, _, _ := setupArmedSession(t, 3, 100_000, 10)
	p, known := session.sm.HeightSyncFloorPointAsOf(session.Nonce() + 1)
	require.True(t, known)
	require.NotZero(t, p.Height)
	require.Positive(t, p.TimeMs, "armed session must carry floor TimeMs for the empty-map path")

	session.sm.SetHeaderTimes(headertime.New())

	prepared, err := session.PrepareInference(context.Background(), InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	require.NotNil(t, prepared)

	var start *types.MsgStartInference
	for _, tx := range prepared.diff.Txs {
		if s := tx.GetStartInference(); s != nil {
			start = s
			break
		}
	}
	require.NotNil(t, start)
	require.Equal(t, p.Height, start.ObservedHeight)
	require.Equal(t, p.TimeMs, start.ObservedTimeMs)
}

func TestHeartbeatCompose_EmptyMapUsesFloorTime(t *testing.T) {
	session, _, _, _ := setupArmedSession(t, 3, 100_000, 10)
	p, known := session.sm.HeightSyncFloorPointAsOf(session.Nonce() + 1)
	require.True(t, known)
	require.Positive(t, p.TimeMs)

	session.sm.SetHeaderTimes(headertime.New())

	span, err := session.composeSpan(true)
	require.NoError(t, err)
	require.NotEmpty(t, span)

	var sawHB bool
	for _, c := range span {
		for _, tx := range c.diff.Txs {
			if hb := tx.GetHeartbeat(); hb != nil {
				sawHB = true
				require.Equal(t, p.Height, hb.ObservedHeight)
				require.Equal(t, p.TimeMs, hb.ObservedTimeMs)
			}
		}
	}
	require.True(t, sawHB, "forced span must include heartbeats")
}
