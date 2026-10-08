package state

import (
	"fmt"
	"maps"
	"testing"

	"devshard/types"
)

// benchRollbackWrites is the write set of one benchmark diff: this many
// record updates (each re-marshalled into committedEntries) plus one
// host-stats slot.
const benchRollbackWrites = 4

// BenchmarkRollbackCopyVsJournal isolates the rollback mechanism. Both sides
// run the same writes on a machine with n live Finished records and n sealed
// ids; only the way apply can undo them differs.
//
//   - copy: the deep copy apply used before the journal (copyRollback).
//   - journal: beginJournalLocked and the write helpers.
//
// Scenarios:
//
//   - failed: open, write, roll back. One copy per diff.
//   - trial-commit: the ValidateDiff / PreviewLocalBestEffort shape, then
//     CommitValidated. The copy side takes the pre, inner, and post copies
//     and installs post; the journal side detaches and redoes.
func BenchmarkRollbackCopyVsJournal(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 30 * 60 * 32 / 2} {
		b.Run(fmt.Sprintf("failed/copy/n=%d", n), func(b *testing.B) {
			sm := newBenchApplyEnv(b).machine(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				snap := copyRollbackOf(sm)
				benchRollbackWrite(b, sm, i, false)
				snap.restore(sm)
			}
			benchRollbackCheck(b, sm, 0)
		})
		b.Run(fmt.Sprintf("failed/journal/n=%d", n), func(b *testing.B) {
			sm := newBenchApplyEnv(b).machine(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				j, err := sm.beginJournalLocked()
				if err != nil {
					b.Fatal(err)
				}
				benchRollbackWrite(b, sm, i, true)
				sm.closeJournalLocked(j)
			}
			benchRollbackCheck(b, sm, 0)
		})
		b.Run(fmt.Sprintf("trial-commit/copy/n=%d", n), func(b *testing.B) {
			sm := newBenchApplyEnv(b).machine(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				pre := copyRollbackOf(sm)
				_ = copyRollbackOf(sm)
				benchRollbackWrite(b, sm, i, false)
				post := copyRollbackOf(sm)
				pre.restore(sm)
				post.restore(sm)
			}
			benchRollbackCheck(b, sm, uint32(b.N-1))
		})
		b.Run(fmt.Sprintf("trial-commit/journal/n=%d", n), func(b *testing.B) {
			sm := newBenchApplyEnv(b).machine(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				j, err := sm.beginJournalLocked()
				if err != nil {
					b.Fatal(err)
				}
				benchRollbackWrite(b, sm, i, true)
				j.kept = true
				sm.detachJournalLocked(j)
				sm.closeJournalLocked(j)
				sm.redoJournalLocked(j)
			}
			benchRollbackCheck(b, sm, uint32(b.N-1))
		})
	}
}

// benchRollbackWrite updates benchRollbackWrites records and one host-stats
// slot. The vote count changes every iteration so the committed bytes differ.
func benchRollbackWrite(b *testing.B, sm *StateMachine, i int, journaled bool) {
	b.Helper()
	for k := 0; k < benchRollbackWrites; k++ {
		id := uint64(k + 1)
		var rec *types.InferenceRecord
		if journaled {
			rec, _ = sm.inferenceForWriteLocked(id)
		} else {
			rec = sm.state.Inferences[id]
		}
		rec.VotesValid = uint32(i)
		if err := sm.updateCommittedEntryLocked(id, rec); err != nil {
			b.Fatal(err)
		}
	}
	var hs *types.HostStats
	if journaled {
		hs = sm.hostStatsForWriteLocked(0)
	} else {
		hs = sm.state.HostStats[0]
	}
	if hs != nil {
		hs.Cost++
	}
	sm.state.Balance--
}

// benchRollbackCheck fails the benchmark if the written records do not hold
// votes or the running point sum no longer matches the committed blobs.
func benchRollbackCheck(b *testing.B, sm *StateMachine, votes uint32) {
	b.Helper()
	b.StopTimer()
	for k := 0; k < benchRollbackWrites; k++ {
		if got := sm.state.Inferences[uint64(k+1)].VotesValid; got != votes {
			b.Fatalf("inference %d votes %d, want %d", k+1, got, votes)
		}
	}
	if sm.liveEntrySum != sumLivePointsFromEntries(sm.committedEntries) {
		b.Fatal("running point sum does not match committed entries")
	}
}

// copyRollback is a full copy of every field apply can write: each record,
// each committed blob, sealedNonces, host stats, and warm keys.
type copyRollback struct {
	balance, fees, finalizeNonce, latestNonce uint64
	phase                                     types.SessionPhase
	liveEntrySum                              [32]byte
	inferences                                map[uint64]*types.InferenceRecord
	committed                                 map[uint64][]byte
	hostStats                                 map[uint32]*types.HostStats
	warmKeys                                  map[uint32]string
	sealedAcc                                 []byte
	sealedNonces                              map[uint64]uint64
}

func copyRollbackOf(sm *StateMachine) copyRollback {
	st := sm.state
	hostStats := make(map[uint32]*types.HostStats, len(st.HostStats))
	for k, v := range st.HostStats {
		cp := *v
		hostStats[k] = &cp
	}
	warmKeys := make(map[uint32]string, len(st.WarmKeys))
	maps.Copy(warmKeys, st.WarmKeys)
	sealedNonces := make(map[uint64]uint64, len(sm.sealedNonces))
	maps.Copy(sealedNonces, sm.sealedNonces)
	return copyRollback{
		balance:       st.Balance,
		fees:          st.Fees,
		finalizeNonce: st.FinalizeNonce,
		latestNonce:   st.LatestNonce,
		phase:         st.Phase,
		liveEntrySum:  sm.liveEntrySum,
		inferences:    copyInferences(st.Inferences),
		committed:     cloneCommittedInferenceEntries(sm.committedEntries),
		hostStats:     hostStats,
		warmKeys:      warmKeys,
		sealedAcc:     append([]byte(nil), st.SealedAcc...),
		sealedNonces:  sealedNonces,
	}
}

func (c copyRollback) restore(sm *StateMachine) {
	st := sm.state
	st.Balance = c.balance
	st.Fees = c.fees
	st.FinalizeNonce = c.finalizeNonce
	st.LatestNonce = c.latestNonce
	st.Phase = c.phase
	sm.liveEntrySum = c.liveEntrySum
	st.Inferences = c.inferences
	sm.committedEntries = c.committed
	st.HostStats = c.hostStats
	st.WarmKeys = c.warmKeys
	st.SealedAcc = append([]byte(nil), c.sealedAcc...)
	sm.sealedNonces = c.sealedNonces
}
