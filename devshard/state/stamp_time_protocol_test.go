package state

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks/headertime"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func newProtocolFloorDriver(t *testing.T, version string) *floorDriver {
	t.Helper()
	hosts := fourHosts(t)
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	sm, err := NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(),
		signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1_000_000),
		WithStateRootAndProtocolVersion(version))
	require.NoError(t, err)
	return &floorDriver{t: t, sm: sm, user: user, hosts: hosts}
}

// Step 8: v7 rejects a stamped start with observed_time_ms == 0.
func TestV7_StampTimeZeroRejected(t *testing.T) {
	d := newProtocolFloorDriver(t, types.DevshardStateRootAndProtocolVersion)
	require.True(t, types.StampRequiresTime(d.sm.ProtocolVersion()))
	hash := []byte{0x81}
	d.raiseWithTime(100, hash, time.Hour.Milliseconds())
	id := d.next()
	err := d.apply(divStartTx(id, 100, hash))
	require.ErrorIs(t, err, types.ErrStampTimeMissing)
	require.NotErrorIs(t, err, types.ErrHeaderPending)
	_, ok := d.sm.GetInference(id)
	require.False(t, ok)
}

// Step 8: v6 still accepts zero stamp times.
func TestV6_StampTimeZeroStillApplies(t *testing.T) {
	d := newProtocolFloorDriver(t, "v6")
	require.False(t, types.StampRequiresTime(d.sm.ProtocolVersion()))
	require.False(t, types.StartRequiresFloor(d.sm.ProtocolVersion()))
	hash := []byte{0x82}
	d.raiseWithTime(100, hash, time.Hour.Milliseconds())
	id := d.next()
	start := divStartTx(id, 100, hash)
	require.Zero(t, start.GetStartInference().GetObservedTimeMs())
	require.NoError(t, d.apply(start))
	require.NoError(t, d.apply(d.confirm(id, 100, hash)))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Zero(t, rec.StartedAtTimeMs)
	require.Zero(t, rec.ConfirmedAtTimeMs)
}

// Step 8: v7 start→confirm→finish→auto-seal with empty map; roots agree.
func TestV7_EmptyMapSealRootsAgree(t *testing.T) {
	build := func(t *testing.T) *floorDriver {
		t.Helper()
		d := newProtocolFloorDriver(t, types.DevshardStateRootAndProtocolVersion)
		d.sm.SetHeaderTimes(headertime.New())
		d.sm.mu.Lock()
		d.sm.state.Config.AutoSealEveryNNonces = 1
		d.sm.state.Config.InferenceSealGraceNonces = 0
		d.sm.state.Config.InferenceSealGraceSeconds = 1
		d.sm.state.Config.ExecutionTimeout = 0
		d.sm.mu.Unlock()

		hash := []byte{0x83}
		confirmAt := time.Hour
		d.raiseWithTime(1000, hash, confirmAt.Milliseconds())
		id := d.next()
		require.NoError(t, d.apply(startTxWithTime(id, 1000, hash, confirmAt.Milliseconds())))
		require.NoError(t, d.apply(d.confirmWithTime(id, 1000, hash, confirmAt.Milliseconds())))

		finish := &types.MsgFinishInference{
			InferenceId: id, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash,
			InputTokens: 10, OutputTokens: 20, ExecutorSlot: uint32(id % uint64(len(d.hosts))), EscrowId: "escrow-1",
			ObservedHeight: 1000, ObservedBlockHash: hash, ObservedTimeMs: confirmAt.Milliseconds(),
		}
		finish.ProposerSig = testutil.SignProposerTx(t, d.hosts[finish.ExecutorSlot], finish)
		require.NoError(t, d.apply(txFinish(finish)))

		grace := int64(d.sm.Config().InferenceSealGraceSeconds)
		exec := d.sm.Config().ExecutionTimeout
		required := time.Duration(FinishedClockRequiredSeconds(grace, exec)) * time.Second
		d.raiseWithTime(2000, []byte{0x84}, (confirmAt + required).Milliseconds())
		// Empty padded diffs until auto-seal fires on the Finished clock gate.
		for {
			_, live := d.sm.GetInference(id)
			if !live {
				break
			}
			require.NoError(t, d.apply())
			require.Less(t, d.sm.LatestNonce(), id+100, "auto-seal should clear Finished soon")
		}
		require.Contains(t, d.sm.ExportSealedNonces(), id)
		return d
	}

	a := build(t)
	b := build(t)
	rootA, err := a.sm.ComputeStateRoot()
	require.NoError(t, err)
	rootB, err := b.sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, rootA, rootB, "empty maps: identical carried times → identical roots")
}

// Step 8: canonical entry still omits carried times on v7 (root commits to heights/hashes).
func TestV7_CanonicalInferenceEntryOmitsStampTimes(t *testing.T) {
	base := &types.InferenceRecord{
		Status:             types.StatusStarted,
		ExecutorSlot:       1,
		Model:              "llama",
		PromptHash:         []byte("prompt"),
		InputLength:        100,
		MaxTokens:          testutil.TestMaxTokens,
		ReservedCost:       100,
		StartedAtHeight:    100,
		StartedAtBlockHash: []byte{0xaa},
		ConfirmedAtHeight:  101,
	}
	withTimes := *base
	withTimes.StartedAtTimeMs = time.Hour.Milliseconds()
	withTimes.ConfirmedAtTimeMs = (2 * time.Hour).Milliseconds()

	a, err := marshalInferenceEntry(1, base)
	require.NoError(t, err)
	b, err := marshalInferenceEntry(1, &withTimes)
	require.NoError(t, err)
	require.Equal(t, a, b, "carried times stay out of the state-root entry on v7")
}
