package registry

import (
	"slices"
	"sync"

	"devshard/types"
)

const finishedRereadsPerRead = 512

// OpenRecordReader reads one session's records by id; sessionHandle satisfies it. See README.md, "Reading an escrow's open records".
type OpenRecordReader interface {
	SessionConfig() types.SessionConfig
	AppliedNonce() uint64
	LiveInferenceIDs() map[uint64]struct{}
	Inference(id uint64) (types.InferenceRecord, bool)
}

type openRecordIndex struct {
	mu       sync.Mutex
	started  bool
	examined uint64
	open     map[uint64]struct{}
	finished []uint64
	cursor   int
}

// OpenRecords: see README.md, "Reading an escrow's open records".
func (r *Registry) OpenRecords(escrowID string) (types.SessionConfig, []types.InferenceRecord, bool) {
	entry, live := r.live.Load().byID[escrowID]
	if !live {
		return types.SessionConfig{}, nil, false
	}
	reader, indexed := entry.session.(OpenRecordReader)
	if !indexed {
		config, records := entry.session.LiveInferences()
		return config, slices.DeleteFunc(records, func(record types.InferenceRecord) bool { return !isOpenRecord(record.Status) }), true
	}
	return reader.SessionConfig(), entry.openRecords.read(reader), true
}

func (index *openRecordIndex) read(reader OpenRecordReader) []types.InferenceRecord {
	index.mu.Lock()
	defer index.mu.Unlock()
	applied := reader.AppliedNonce()
	if !index.started {
		index.started, index.open = true, map[uint64]struct{}{}
		for id := range reader.LiveInferenceIDs() {
			index.open[id] = struct{}{}
			applied = max(applied, id)
		}
	} else {
		for id := index.examined + 1; id <= applied; id++ {
			index.open[id] = struct{}{}
		}
	}
	index.examined = max(index.examined, applied)
	records := index.readOpen(reader)
	return append(records, index.recheckFinished(reader)...)
}

func (index *openRecordIndex) readOpen(reader OpenRecordReader) []types.InferenceRecord {
	records := make([]types.InferenceRecord, 0, len(index.open))
	for id := range index.open {
		record, live := reader.Inference(id)
		switch {
		case live && isOpenRecord(record.Status):
			records = append(records, record)
		case live && record.Status == types.StatusFinished:
			index.finished = append(index.finished, id)
			delete(index.open, id)
		default:
			delete(index.open, id)
		}
	}
	return records
}

func (index *openRecordIndex) recheckFinished(reader OpenRecordReader) []types.InferenceRecord {
	end := min(index.cursor+finishedRereadsPerRead, len(index.finished))
	kept := index.finished[:index.cursor]
	var reopened []types.InferenceRecord
	for _, id := range index.finished[index.cursor:end] {
		record, live := reader.Inference(id)
		switch {
		case live && record.Status == types.StatusFinished:
			kept = append(kept, id)
		case live && isOpenRecord(record.Status):
			index.open[id] = struct{}{}
			reopened = append(reopened, record)
		}
	}
	index.cursor = len(kept)
	index.finished = append(kept, index.finished[end:]...)
	if index.cursor >= len(index.finished) {
		index.cursor = 0
	}
	return reopened
}

func isOpenRecord(status types.InferenceStatus) bool {
	return status == types.StatusPending || status == types.StatusStarted || status == types.StatusChallenged
}
