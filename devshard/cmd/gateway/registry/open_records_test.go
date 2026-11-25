package registry

import (
	"context"
	"slices"
	"sync"
	"testing"

	"devshard/types"
)

// fakeRecordReader serves records by id and counts every single-record read.
type fakeRecordReader struct {
	applied uint64
	records map[uint64]types.InferenceRecord
	reads   int
}

func newFakeRecordReader() *fakeRecordReader {
	return &fakeRecordReader{records: map[uint64]types.InferenceRecord{}}
}

func (f *fakeRecordReader) SessionConfig() types.SessionConfig {
	return types.SessionConfig{RefusalTimeout: 60, ExecutionTimeout: 1800}
}

func (f *fakeRecordReader) AppliedNonce() uint64 { return f.applied }

func (f *fakeRecordReader) LiveInferenceIDs() map[uint64]struct{} {
	ids := make(map[uint64]struct{}, len(f.records))
	for id := range f.records {
		ids[id] = struct{}{}
	}
	return ids
}

func (f *fakeRecordReader) Inference(id uint64) (types.InferenceRecord, bool) {
	f.reads++
	record, live := f.records[id]
	return record, live
}

// add stores a record whose reserved cost is its own id, so a result names the records it holds.
func (f *fakeRecordReader) add(id uint64, status types.InferenceStatus) {
	f.records[id] = types.InferenceRecord{Status: status, ReservedCost: id}
	f.applied = max(f.applied, id)
}

func markedIDs(records []types.InferenceRecord) []uint64 {
	ids := make([]uint64, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ReservedCost)
	}
	slices.Sort(ids)
	return ids
}

// Test flow:
//  1. Store one record of every status and read the index once.
//  2. Assert it returns the Pending, Started and Challenged records and nothing else.
func TestTheOpenRecordIndexReturnsPendingStartedAndChallengedRecordsOnly(t *testing.T) {
	t.Parallel()
	reader := newFakeRecordReader()
	reader.add(1, types.StatusFinished)
	reader.add(2, types.StatusPending)
	reader.add(3, types.StatusStarted)
	reader.add(4, types.StatusValidated)
	reader.add(5, types.StatusChallenged)
	reader.add(6, types.StatusTimedOut)
	reader.add(7, types.StatusInvalidated)
	var index openRecordIndex

	if got := markedIDs(index.read(reader)); !slices.Equal(got, []uint64{2, 3, 5}) {
		t.Fatalf("read() = %v, want [2 3 5]", got)
	}
}

// Test flow:
//  1. Store one Finished record and read the index.
//  2. Turn the record Challenged, as a late invalid vote does, and read again.
//  3. Assert the second read returns it.
func TestAFinishedRecordChallengedLaterIsFoundOnTheNextRead(t *testing.T) {
	t.Parallel()
	reader := newFakeRecordReader()
	reader.add(1, types.StatusFinished)
	var index openRecordIndex
	if got := index.read(reader); len(got) != 0 {
		t.Fatalf("first read() = %v, want none", markedIDs(got))
	}

	reader.records[1] = types.InferenceRecord{Status: types.StatusChallenged, ReservedCost: 1}

	if got := markedIDs(index.read(reader)); !slices.Equal(got, []uint64{1}) {
		t.Fatalf("second read() = %v, want [1]", got)
	}
}

// Test flow:
//  1. Store one Pending record and read the index.
//  2. Remove the record, as sealing does, and read again.
//  3. Assert the second read returns nothing and the index no longer tracks the id.
func TestARecordSealedAwayIsForgotten(t *testing.T) {
	t.Parallel()
	reader := newFakeRecordReader()
	reader.add(2, types.StatusPending)
	var index openRecordIndex
	index.read(reader)

	delete(reader.records, 2)

	if got := index.read(reader); len(got) != 0 {
		t.Fatalf("read() after sealing = %v, want none", markedIDs(got))
	}
	if _, tracked := index.open[2]; tracked {
		t.Fatal("index.open still tracks a sealed record, want it forgotten")
	}
}

// Test flow:
//  1. Store ten thousand records, two of them open, and read the index once.
//  2. Start one new record and read again, counting single-record reads.
//  3. Assert the second read touched only the two open records, the new nonce and one slice of 512 Finished records, and returned the three open ones.
func TestAReadAfterTheFirstTouchesNewNoncesOpenRecordsAndOneFinishedSlice(t *testing.T) {
	t.Parallel()
	reader := newFakeRecordReader()
	for id := uint64(1); id <= 9_998; id++ {
		reader.add(id, types.StatusFinished)
	}
	reader.add(9_999, types.StatusPending)
	reader.add(10_000, types.StatusStarted)
	var index openRecordIndex
	index.read(reader)
	reader.reads = 0

	reader.add(10_001, types.StatusPending)
	got := index.read(reader)

	if reader.reads != 2+1+finishedRereadsPerRead {
		t.Fatalf("Inference() calls = %d, want %d", reader.reads, 2+1+finishedRereadsPerRead)
	}
	if ids := markedIDs(got); !slices.Equal(ids, []uint64{9_999, 10_000, 10_001}) {
		t.Fatalf("read() = %v, want [9999 10000 10001]", ids)
	}
}

// indexedFakeSession is a registry fake that also offers single-record reads.
type indexedFakeSession struct {
	*fakeSession
	*fakeRecordReader
}

func (s indexedFakeSession) Inference(id uint64) (types.InferenceRecord, bool) {
	return s.fakeRecordReader.Inference(id)
}

func (s indexedFakeSession) SealedInference(uint64) (types.InferenceRecord, bool) {
	return types.InferenceRecord{}, false
}

// Test flow:
//  1. Publish one escrow whose session offers record reads and one whose session does not, both holding a Finished and a Pending record.
//  2. Ask the registry for each escrow's open records, and for an escrow it never published.
//  3. Assert both answer the Pending record alone, the indexed one through its reader, and the unknown escrow answers not known.
func TestRegistryOpenRecordsReadsThroughTheIndexOrFallsBackToTheLiveWindow(t *testing.T) {
	t.Parallel()
	plain := newFakeSession("hostA", "hostB")
	plain.escrowState = types.EscrowState{Inferences: map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusFinished, ReservedCost: 1},
		2: {Status: types.StatusPending, ReservedCost: 2},
	}}
	reader := newFakeRecordReader()
	reader.add(1, types.StatusFinished)
	reader.add(2, types.StatusPending)
	indexed := indexedFakeSession{fakeSession: newFakeSession("hostA", "hostB"), fakeRecordReader: reader}
	registry := New(Deps{
		ServingSessions: func(_ context.Context, escrowID string) (EscrowSession, error) {
			if escrowID == "6" {
				return indexed, nil
			}
			return plain, nil
		},
		Now: fixedClock(),
	})
	mustAdd(t, registry, "5", "qwen")
	mustAdd(t, registry, "6", "qwen")

	for _, escrowID := range []string{"5", "6"} {
		_, records, known := registry.OpenRecords(escrowID)
		if got := markedIDs(records); !known || !slices.Equal(got, []uint64{2}) {
			t.Fatalf("OpenRecords(%s) = %v, %v, want [2], true", escrowID, got, known)
		}
	}
	if reader.reads == 0 {
		t.Fatal("OpenRecords(6) never read through the session's record reader, want the index used")
	}
	if _, _, known := registry.OpenRecords("9"); known {
		t.Fatal("OpenRecords(9) = known, want an unpublished escrow unknown")
	}
}

// Test flow:
//  1. Store two Finished records while the applied nonce still names only the first, and read the index.
//  2. Advance the applied nonce to the second record and turn it Challenged.
//  3. Assert the next read returns the record once and the finished list holds each id once.
func TestARecordAboveTheAppliedNonceOnTheFirstReadIsNotCountedTwice(t *testing.T) {
	t.Parallel()
	reader := newFakeRecordReader()
	reader.add(1, types.StatusFinished)
	reader.add(2, types.StatusFinished)
	reader.applied = 1
	var index openRecordIndex
	index.read(reader)

	reader.applied = 2
	reader.records[2] = types.InferenceRecord{Status: types.StatusChallenged, ReservedCost: 2}
	got := markedIDs(index.read(reader))

	if !slices.Equal(got, []uint64{2}) {
		t.Fatalf("read() = %v, want [2]", got)
	}
	if finished := slices.Sorted(slices.Values(index.finished)); !slices.Equal(finished, []uint64{1}) {
		t.Fatalf("index.finished = %v, want [1]", finished)
	}
}

// Test flow:
//  1. Store two thousand Finished records and read the index once.
//  2. Turn Challenged the id the rotation reaches last.
//  3. Assert the next read misses it and a later one within ceil(finished/512) reads returns it.
func TestALateChallengePastTheFirstSliceSurfacesWithinOneRotation(t *testing.T) {
	t.Parallel()
	const finishedCount = 2_000
	reader := newFakeRecordReader()
	for id := uint64(1); id <= finishedCount; id++ {
		reader.add(id, types.StatusFinished)
	}
	var index openRecordIndex
	index.read(reader)
	challenged := index.finished[len(index.finished)-1]

	reader.records[challenged] = types.InferenceRecord{Status: types.StatusChallenged, ReservedCost: challenged}

	rotation := (finishedCount + finishedRereadsPerRead - 1) / finishedRereadsPerRead
	foundOnRead := 0
	for read := 1; read <= rotation && foundOnRead == 0; read++ {
		if got := markedIDs(index.read(reader)); slices.Equal(got, []uint64{challenged}) {
			foundOnRead = read
		}
	}
	if foundOnRead <= 1 {
		t.Fatalf("read() found the challenged record on read %d, want after the first and within %d", foundOnRead, rotation)
	}
}

// Test flow:
//  1. Store one Pending record and read the index.
//  2. Advance the applied nonce past two nonces that left no record, and read again.
//  3. Assert the read returns the Pending record alone and the index tracks neither empty nonce.
func TestANonceWithNoRecordIsDropped(t *testing.T) {
	t.Parallel()
	reader := newFakeRecordReader()
	reader.add(1, types.StatusPending)
	var index openRecordIndex
	index.read(reader)

	reader.applied = 3
	got := markedIDs(index.read(reader))

	if !slices.Equal(got, []uint64{1}) {
		t.Fatalf("read() = %v, want [1]", got)
	}
	for _, empty := range []uint64{2, 3} {
		if _, tracked := index.open[empty]; tracked {
			t.Fatalf("index.open tracks nonce %d with no record, want it dropped", empty)
		}
	}
}

// Test flow:
//  1. Publish one escrow whose session offers record reads, holding a Finished and a Pending record.
//  2. Ask the registry for its open records from eight goroutines at once, fifty times each.
//  3. Assert every answer is the Pending record alone.
func TestConcurrentOpenRecordsCallsAgree(t *testing.T) {
	t.Parallel()
	reader := newFakeRecordReader()
	reader.add(1, types.StatusFinished)
	reader.add(2, types.StatusPending)
	indexed := indexedFakeSession{fakeSession: newFakeSession("hostA", "hostB"), fakeRecordReader: reader}
	registry := New(Deps{
		ServingSessions: func(context.Context, string) (EscrowSession, error) { return indexed, nil },
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "6", "qwen")

	var callers sync.WaitGroup
	for range 8 {
		callers.Go(func() {
			for range 50 {
				_, records, known := registry.OpenRecords("6")
				if got := markedIDs(records); !known || !slices.Equal(got, []uint64{2}) {
					t.Errorf("OpenRecords(6) = %v, %v, want [2], true", got, known)
					return
				}
			}
		})
	}
	callers.Wait()
}
