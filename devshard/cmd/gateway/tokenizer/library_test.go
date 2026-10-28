package tokenizer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeSource struct {
	commit string
	err    error
	reads  int
}

func (s *fakeSource) GetModelSource(_ context.Context, _ uint64, model string) (string, string, error) {
	s.reads++
	return "org/" + model, s.commit, s.err
}

type fakeHub struct {
	status  int
	fetches int
	body    []byte
}

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "isolated.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &fakeHub{status: http.StatusOK, body: body}
}

func (h *fakeHub) RoundTrip(request *http.Request) (*http.Response, error) {
	h.fetches++
	status, body := h.status, h.body
	if request.URL.Host != "huggingface.co" || !strings.HasPrefix(request.URL.Path, "/org/m/resolve/") || !strings.HasSuffix(request.URL.Path, "/tokenizer.json") {
		status = http.StatusNotFound
	}
	if status != http.StatusOK {
		body = nil
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: request}, nil
}

type testClock struct{ now time.Time }

func (c *testClock) read() time.Time          { return c.now }
func (c *testClock) advance(by time.Duration) { c.now = c.now.Add(by) }

type testLibrary struct {
	*Library
	epoch      uint64
	epochKnown bool
	pending    []func()
}

func newTestLibrary(source ModelSource, hub *fakeHub, epoch uint64, clock *testClock) *testLibrary {
	library := &testLibrary{epoch: epoch, epochKnown: true}
	library.Library = NewLibrary(source, func() (uint64, bool) { return library.epoch, library.epochKnown })
	library.httpClient = &http.Client{Transport: hub}
	library.now = clock.read
	library.background = func(task func()) { library.pending = append(library.pending, task) }
	return library
}

func (l *testLibrary) runPending() int {
	tasks := l.pending
	l.pending = nil
	for _, task := range tasks {
		task()
	}
	return len(tasks)
}

func (l *testLibrary) loaded() bool {
	return l.Counter("m") != nil
}

// Test flow:
//  1. Build a library over a fake chain pin and a fake hub serving a tokenizer.json at the pinned commit.
//  2. Ask for the model's counter: assert the first call has none and queues one load.
//  3. Run the load and ask again: assert the counter counts as the pinned tokenizer does.
//  4. Move to a new epoch that pins the same commit: assert the pin is read again and the tokenizer is neither fetched again nor dropped.
//  5. Pin a new commit in the next epoch: assert the tokenizer is fetched again.
func TestLibraryLoadsThePinnedTokenizerOnceAPin(t *testing.T) {
	source := &fakeSource{commit: "c1"}
	hub := newFakeHub(t)
	library := newTestLibrary(source, hub, 5, &testClock{})

	if library.loaded() {
		t.Fatal("a counter is held before any tokenizer loaded")
	}
	if loads := library.runPending(); loads != 1 {
		t.Fatalf("loads queued = %d, want 1", loads)
	}
	got, err := library.Counter("m").Count(goldenTexts[0])
	if err != nil {
		t.Fatal(err)
	}
	want, err := loadTestdata(t, "isolated").Count(goldenTexts[0])
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Count = %d, want the pinned tokenizer's %d", got, want)
	}

	library.epoch = 6
	library.Counter("m")
	library.runPending()
	if !library.loaded() || source.reads != 2 || hub.fetches != 1 {
		t.Fatalf("loaded = %v, pin reads = %d, fetches = %d after an epoch that kept the pin, want true, 2, 1", library.loaded(), source.reads, hub.fetches)
	}

	source.commit = "c2"
	library.epoch = 7
	library.Counter("m")
	library.runPending()
	if hub.fetches != 2 {
		t.Fatalf("fetches = %d after the pin moved, want 2", hub.fetches)
	}
}

// Test flow:
//  1. Define pins whose tokenizer.json cannot be counted: one the hub answers 404 for, as for a model that ships none, and one whose file does not parse.
//  2. For each, ask for the counter and run the load, then ask again past the retry interval and in a new epoch with the same pin, running every queued load.
//  3. Assert no counter is held and the hub was asked once: a pinned commit never changes.
func TestLibraryDoesNotRefetchAPinWithoutAReadableTokenizer(t *testing.T) {
	for name, breakHub := range map[string]func(*fakeHub){
		"no tokenizer.json":         func(hub *fakeHub) { hub.status = http.StatusNotFound },
		"unreadable tokenizer.json": func(hub *fakeHub) { hub.body = []byte(`{"model":`) },
	} {
		t.Run(name, func(t *testing.T) {
			source := &fakeSource{commit: "c1"}
			hub := newFakeHub(t)
			breakHub(hub)
			clock := &testClock{}
			library := newTestLibrary(source, hub, 5, clock)

			library.Counter("m")
			library.runPending()
			clock.advance(2 * retryInterval)
			library.epoch = 6
			library.Counter("m")
			library.runPending()

			if library.loaded() || source.reads != 2 || hub.fetches != 1 {
				t.Fatalf("loaded = %v, pin reads = %d, fetches = %d, want false, 2, 1", library.loaded(), source.reads, hub.fetches)
			}
		})
	}
}

// Test flow:
//  1. Fail the first load with a 503 from the hub.
//  2. Assert asking again inside the retry interval queues no load.
//  3. Restore the hub, step past the retry interval, and assert the next ask queues a load that leaves a counter held.
func TestLibraryRetriesATransientFailureAfterTheInterval(t *testing.T) {
	source := &fakeSource{commit: "c1"}
	hub := newFakeHub(t)
	hub.status = http.StatusServiceUnavailable
	clock := &testClock{}
	library := newTestLibrary(source, hub, 5, clock)

	library.Counter("m")
	library.runPending()
	library.Counter("m")
	if loads := library.runPending(); loads != 0 || hub.fetches != 1 {
		t.Fatalf("loads = %d, fetches = %d inside the retry interval, want 0, 1", loads, hub.fetches)
	}

	hub.status = http.StatusOK
	clock.advance(retryInterval)
	library.Counter("m")
	library.runPending()
	if !library.loaded() {
		t.Fatal("no counter is held after the retry loaded the tokenizer")
	}
}

// Test flow:
//  1. Build a library whose chain epoch is not known yet, and one whose pin read fails.
//  2. Ask each for the counter, running every queued load.
//  3. Assert neither holds one, the first without reading a pin at all.
func TestLibraryWithoutAPinHoldsNoCounter(t *testing.T) {
	hub := newFakeHub(t)
	unread := &fakeSource{commit: "c1"}
	early := newTestLibrary(unread, hub, 5, &testClock{})
	early.epochKnown = false
	early.Counter("m")
	if loads := early.runPending(); early.loaded() || loads != 0 || unread.reads != 0 {
		t.Fatalf("loaded = %v, loads = %d, pin reads = %d before the epoch is known, want false, 0, 0", early.loaded(), loads, unread.reads)
	}

	library := newTestLibrary(&fakeSource{err: errors.New("chain down")}, hub, 5, &testClock{})
	library.Counter("m")
	library.runPending()
	if library.loaded() {
		t.Fatal("a model whose pin could not be read holds a counter")
	}
}

// Test flow:
//  1. Build a library whose tokenizer.json parser panics.
//  2. Ask for the counter and run the load, then ask again past the retry interval and in a new epoch with the same pin, running every queued load.
//  3. Assert the panic did not escape the load, no counter is held, and the hub was asked once, as for a file that does not load.
func TestLibraryRecordsAPanickingLoadAsUnsupported(t *testing.T) {
	source := &fakeSource{commit: "c1"}
	hub := newFakeHub(t)
	clock := &testClock{}
	library := newTestLibrary(source, hub, 5, clock)
	library.parse = func(io.Reader) (*Counter, error) { panic("malformed merges") }

	library.Counter("m")
	library.runPending()
	clock.advance(2 * retryInterval)
	library.epoch = 6
	library.Counter("m")
	library.runPending()

	if library.loaded() || source.reads != 2 || hub.fetches != 1 {
		t.Fatalf("loaded = %v, pin reads = %d, fetches = %d, want false, 2, 1", library.loaded(), source.reads, hub.fetches)
	}
}

// Test flow:
//  1. Load the tokenizer pinned at c1.
//  2. In the next epoch, fail the pin read: assert the c1 tokenizer still counts.
//  3. Past the retry interval, pin c2 and fail its fetch with a 503: assert no counter is held, so the c1 vocabulary no longer counts.
//  4. Restore the hub and step past the retry interval: assert the c2 tokenizer loads.
func TestLibraryDropsTheOldTokenizerOnlyWhenTheNewPinFailsToLoad(t *testing.T) {
	source := &fakeSource{commit: "c1"}
	hub := newFakeHub(t)
	clock := &testClock{}
	library := newTestLibrary(source, hub, 5, clock)
	library.Counter("m")
	library.runPending()

	source.err = errors.New("chain down")
	library.epoch = 6
	library.Counter("m")
	library.runPending()
	if !library.loaded() {
		t.Fatal("a pin read that failed dropped the loaded tokenizer")
	}

	source.err, source.commit = nil, "c2"
	hub.status = http.StatusServiceUnavailable
	clock.advance(retryInterval)
	library.Counter("m")
	library.runPending()
	if library.loaded() {
		t.Fatal("the c1 tokenizer still counts after the pin moved to c2 and c2 failed to load")
	}

	hub.status = http.StatusOK
	clock.advance(retryInterval)
	library.Counter("m")
	library.runPending()
	if !library.loaded() || hub.fetches != 3 {
		t.Fatalf("loaded = %v, fetches = %d after the retry, want true, 3", library.loaded(), hub.fetches)
	}
}
