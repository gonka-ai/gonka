package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/filters"
)

const (
	testLogLimit      = 1 << 20
	testLogGeneration = 0
)

func testLog(index *streamIndex, budget *BufferBudget, limit int64) *streamLog {
	return newStreamLog(cacheKey{model: "qwen"}, limit, budget, index, testLogGeneration)
}

// Test flow:
//  1. Give a log a small limit and a shared budget, and append past it.
//  2. Assert the log dropped itself, returned what it held to the budget, cannot be adopted and is refused for storage.
func TestALogPastItsLimitIsDroppedAndReturnsItsBudget(t *testing.T) {
	budget := NewBufferBudget(1 << 20)
	index := newStreamIndex()
	log := testLog(index, budget, 16)

	log.append([]byte("data: 1\n\n"))
	if budget.Held() == 0 {
		t.Fatal("a live log holds no budget")
	}
	log.append([]byte("data: 2\n\n"))

	if budget.Held() != 0 {
		t.Fatalf("budget holds %d after the log dropped, want 0", budget.Held())
	}
	log.detachSocket()
	if _, adopted := index.adopt(log.key, log.generation); adopted {
		t.Fatal("a dropped log was adopted")
	}
	if _, refusal := log.conclude("7", nil); refusal != cacheRefusedTooLarge {
		t.Fatalf("conclude refusal = %q, want %q", refusal, cacheRefusedTooLarge)
	}
}

// Test flow:
//  1. Let a reader take the first event of a log, then append past the log's limit so it drops while the reader follows.
//  2. Pump the reader into a recorder.
//  3. Assert the reader ends on an error event and not on a clean end or a terminator.
func TestAReaderOfALogDroppedMidReplyEndsOnAnErrorEvent(t *testing.T) {
	index := newStreamIndex()
	log := testLog(index, nil, 16)
	log.append([]byte("data: 1\n\n"))
	log.detachSocket()
	reader, adopted := index.adopt(log.key, log.generation)
	if !adopted {
		t.Fatal("a detached log was not adopted")
	}
	reader.next()
	log.append([]byte("data: 2\n\n"))
	recorder := httptest.NewRecorder()

	written := pumpLog(t.Context(), recorder, reader)

	body := recorder.Body.String()
	if !strings.HasPrefix(body, "data: {\"error\"") || strings.Contains(body, "[DONE]") || written != int64(len(body)) {
		t.Fatalf("reply after the drop = %q (%d bytes counted), want one error event and no terminator", body, written)
	}
}

// Test flow:
//  1. Conclude a log holding the shared budget as stored and check the entry and the budget.
//  2. Conclude a failed race, a log with no body and a log with no escrow.
//  3. Assert each is refused for its own reason.
func TestAConcludedLogIsStoredOrRefusedForTheReasonItsRaceGives(t *testing.T) {
	budget := NewBufferBudget(1 << 20)
	index := newStreamIndex()
	stored := testLog(index, budget, testLogLimit)
	stored.append([]byte("data: 1\n\n"))
	stored.append([]byte("data: [DONE]\n\n"))
	failed := testLog(index, nil, testLogLimit)
	failed.append([]byte("data: 1\n\n"))
	empty := testLog(index, nil, testLogLimit)
	unpinned := testLog(index, nil, testLogLimit)
	unpinned.append([]byte("data: 1\n\n"))

	entry, refusal := stored.conclude("7", nil)
	if refusal != filters.CacheStorable || !entry.stream || entry.escrowID != "7" || string(entry.body) != "data: 1\n\ndata: [DONE]\n\n" || len(entry.bounds) != 2 {
		t.Fatalf("stored entry = %+v refusal %q, want a two-write stream entry under escrow 7", entry, refusal)
	}
	if budget.Held() != 0 {
		t.Fatalf("budget holds %d after the log concluded, want 0", budget.Held())
	}
	if _, refusal := failed.conclude("7", engine.ErrAllAttemptsFailed); refusal != cacheRefusedRaceFailed {
		t.Fatalf("failed race refusal = %q, want %q", refusal, cacheRefusedRaceFailed)
	}
	if _, refusal := empty.conclude("7", nil); refusal != filters.CacheRefusedEmptyBody {
		t.Fatalf("empty log refusal = %q, want %q", refusal, filters.CacheRefusedEmptyBody)
	}
	if _, refusal := unpinned.conclude("", nil); refusal != cacheRefusedNoEscrow {
		t.Fatalf("unpinned log refusal = %q, want %q", refusal, cacheRefusedNoEscrow)
	}
}

// Test flow:
//  1. Give a log a socket and try to adopt it.
//  2. Detach the socket and adopt it, then try to adopt it a second time.
//  3. Release the reader and assert the log can be adopted again.
func TestALogWithASocketOrAReaderIsNotAdopted(t *testing.T) {
	index := newStreamIndex()
	log := testLog(index, nil, testLogLimit)
	log.append([]byte("data: 1\n\n"))

	if _, adopted := index.adopt(log.key, log.generation); adopted {
		t.Fatal("a log with a live socket was adopted")
	}
	log.detachSocket()
	reader, adopted := index.adopt(log.key, log.generation)
	if !adopted {
		t.Fatal("a log whose client left was not adopted")
	}
	if _, again := index.adopt(log.key, log.generation); again {
		t.Fatal("a log somebody reads was adopted a second time")
	}
	reader.release()
	if _, readopted := index.adopt(log.key, log.generation); !readopted {
		t.Fatal("a log whose reader left was not adoptable again")
	}
}

// Test flow:
//  1. Give a log a small limit, drop it by appending past the limit, and detach its socket.
//  2. Ask the log itself to adopt, as a repeat does that read the index just before the drop.
//  3. Assert the dropped log is refused.
func TestADroppedLogCannotBeAdoptedEvenWhenTheRepeatFoundItBeforeTheDrop(t *testing.T) {
	log := testLog(newStreamIndex(), NewBufferBudget(1<<20), 16)
	log.append([]byte("data: 1\n\n"))
	log.append([]byte("data: 2\n\n"))
	log.detachSocket()

	if _, adopted := log.adopt(log.generation); adopted {
		t.Fatal("a dropped log was adopted")
	}
}

// Test flow:
//  1. Give a log a detached socket and ask it to adopt with its own generation, then with a later one.
//  2. Assert the first adoption is granted and the second is refused, because a log begun before a clear is never adopted.
func TestALogFromBeforeAClearCannotBeAdopted(t *testing.T) {
	older := testLog(newStreamIndex(), nil, testLogLimit)
	older.detachSocket()

	if _, adopted := older.adopt(older.generation + 1); adopted {
		t.Fatal("a log begun before a clear was adopted")
	}
	if _, adopted := older.adopt(older.generation); !adopted {
		t.Fatal("a log of the current generation was refused")
	}
}
