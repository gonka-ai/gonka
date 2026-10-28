package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/engine"
)

func fixedNow() time.Time { return time.Unix(1700000000, 0) }

func chatRequest(t *testing.T) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
}

func capturing(tune ...func(*config.Capture)) func(*config.Config) {
	return func(next *config.Config) {
		next.Capture.Enabled = true
		next.Capture.SampleRate = 1
		for _, adjust := range tune {
			adjust(&next.Capture)
		}
	}
}

func capturedFiles(t *testing.T, storageDir, kind string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(storageDir, captureDirName, kind))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading %s captures: %v", kind, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// Test flow:
//  1. Enable capture on a harness and send a chat completion the filters reject.
//  2. Assert the response is 400.
//  3. Read the single captured file back from disk and decode it as a `capturedRequest`.
//  4. Assert its kind is `captureFilterRejected` and it carries the model, path and error.
//  5. Assert its stored body matches the original rejected request.
func TestAFilterRejectedRequestIsCaptured(t *testing.T) {
	live := newHarness(t, capturing())
	rejected := `{"model":"qwen","messages":[{"role":"wizard","content":"hi"}]}`

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", rejected, callerHeaders("caller-a"))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("filter rejection: got %d (%s)", recorder.Code, recorder.Body.String())
	}
	files := capturedFiles(t, live.storageDir, captureFilterRejected)
	if len(files) != 1 {
		t.Fatalf("captured files: got %d %v, want 1", len(files), files)
	}
	raw, err := os.ReadFile(filepath.Join(live.storageDir, captureDirName, captureFilterRejected, files[0]))
	if err != nil {
		t.Fatalf("reading capture: %v", err)
	}
	var record capturedRequest
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("capture is not valid JSON: %v", err)
	}
	if record.Kind != captureFilterRejected {
		t.Fatalf("kind: got %q, want %q", record.Kind, captureFilterRejected)
	}
	if record.Model != "qwen" || record.Path != "/v1/chat/completions" || record.Error == "" {
		t.Fatalf("capture lost its context: %+v", record)
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, record.Body); err != nil {
		t.Fatalf("captured body is not JSON: %v", err)
	}
	if compacted.String() != rejected {
		t.Fatalf("captured body: got %s, want %s", compacted.String(), rejected)
	}
}

// Test flow:
//  1. Enable capture on a harness and send a chat completion that gets served.
//  2. Assert no filter-rejected capture file was written.
func TestAServedRequestIsNotCaptured(t *testing.T) {
	live := newHarness(t, capturing())

	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	if files := capturedFiles(t, live.storageDir, captureFilterRejected); len(files) != 0 {
		t.Fatalf("a served request was captured: %v", files)
	}
}

// Test flow:
//  1. Enable capture on a harness whose inference fails every attempt with `engine.ErrStopped`.
//  2. Send a chat completion.
//  3. Assert exactly one attempts-failed capture file was written.
func TestARaceThatFailsEveryAttemptIsCaptured(t *testing.T) {
	live := newHarness(t, capturing())
	live.inference.reply = ""
	live.inference.err = engine.ErrStopped

	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	if files := capturedFiles(t, live.storageDir, captureAttemptsFailed); len(files) != 1 {
		t.Fatalf("captured files: got %d %v, want 1", len(files), files)
	}
}

// Test flow:
//  1. Start a harness with capture left at its default (disabled).
//  2. Send a chat completion the filters reject.
//  3. Assert the capture directory was never created on disk.
func TestCaptureIsOffUnlessEnabled(t *testing.T) {
	live := newHarness(t)
	rejected := `{"model":"qwen","messages":[{"role":"wizard","content":"hi"}]}`

	live.request(t, http.MethodPost, "/v1/chat/completions", rejected, callerHeaders("caller-a"))

	if _, err := os.Stat(filepath.Join(live.storageDir, captureDirName)); !os.IsNotExist(err) {
		t.Fatalf("capture wrote to disk while disabled: %v", err)
	}
}

// Test flow:
//  1. Enable capture with a 900-byte cap on a harness.
//  2. Send the same rejected chat completion 20 times.
//  3. Assert some files were captured but fewer than all 20, so the cap tripped partway.
//  4. Assert the total bytes held on disk stay at or under the 900-byte cap.
func TestTheCaptureCapStopsWritingOnceTheBudgetIsSpent(t *testing.T) {
	live := newHarness(t, capturing(func(capture *config.Capture) { capture.MaxBytes = 900 }))
	rejected := `{"model":"qwen","messages":[{"role":"wizard","content":"hi"}]}`

	for range 20 {
		live.request(t, http.MethodPost, "/v1/chat/completions", rejected, callerHeaders("caller-a"))
	}

	files := capturedFiles(t, live.storageDir, captureFilterRejected)
	if len(files) == 0 {
		t.Fatal("the cap blocked every write, so nothing was ever captured")
	}
	if len(files) >= 20 {
		t.Fatalf("the cap never tripped: %d of 20 requests were captured", len(files))
	}
	var held int64
	for _, name := range files {
		info, err := os.Stat(filepath.Join(live.storageDir, captureDirName, captureFilterRejected, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		held += info.Size()
	}
	if held > 900 {
		t.Fatalf("captures hold %d bytes over a 900 byte cap", held)
	}
}

// Test flow:
//  1. Seed a capture directory on disk with one 900-byte file already present.
//  2. Create a `requestCapture` with a 900-byte cap over that same directory.
//  3. Filter-reject one chat request through it.
//  4. Assert the directory still holds only the seeded file, so the cap counts what is already on disk rather than a restart resetting it.
func TestTheCapCountsWhatIsAlreadyOnDisk(t *testing.T) {
	storageDir := t.TempDir()
	captureDir := filepath.Join(storageDir, captureDirName, captureFilterRejected)
	if err := os.MkdirAll(captureDir, captureDirPermissions); err != nil {
		t.Fatalf("seeding the capture directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(captureDir, "earlier.json"), make([]byte, 900), captureFilePermissions); err != nil {
		t.Fatalf("seeding an earlier capture: %v", err)
	}

	capture, err := newRequestCapture(config.Capture{Enabled: true, SampleRate: 1, MaxBytes: 900}, storageDir, fixedNow)
	if err != nil {
		t.Fatalf("newRequestCapture: %v", err)
	}
	capture.filterRejected(chatRequest(t), "request-1", []byte(chatBody), engine.ErrStopped)

	if files := capturedFiles(t, storageDir, captureFilterRejected); !slices.Equal(files, []string{"earlier.json"}) {
		t.Fatalf("a restart reset the cap: %v", files)
	}
}

// Test flow:
//  1. Define a table of sample rates, varying across every request, one in four, one in two, and none.
//  2. For each case, create a `requestCapture` at that rate and filter-reject the same request 20 times.
//  3. Assert the number of captured files matches the expected count for that rate.
func TestSamplingHonoursItsRate(t *testing.T) {
	testCases := []struct {
		name  string
		rate  float64
		want  int
		total int
	}{
		{name: "every request", rate: 1, total: 20, want: 20},
		{name: "one in four", rate: 0.25, total: 20, want: 5},
		{name: "one in two", rate: 0.5, total: 20, want: 10},
		{name: "none", rate: 0, total: 20, want: 0},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			storageDir := t.TempDir()
			capture, err := newRequestCapture(config.Capture{Enabled: true, SampleRate: testCase.rate, MaxBytes: 1 << 20}, storageDir, fixedNow)
			if err != nil {
				t.Fatalf("newRequestCapture: %v", err)
			}

			for range testCase.total {
				capture.filterRejected(chatRequest(t), "request-1", []byte(chatBody), engine.ErrStopped)
			}

			if got := len(capturedFiles(t, storageDir, captureFilterRejected)); got != testCase.want {
				t.Fatalf("captured %d of %d at rate %v, want %d", got, testCase.total, testCase.rate, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Create a `requestCapture` with a 512-byte cap.
//  2. Write 20 records of 64-byte request IDs directly through it.
//  3. Read back its stats.
//  4. Assert no writes failed, some were refused once the cap was reached, some succeeded, and the bytes held stay within the 512-byte cap.
func TestCaptureCountsWhatItRefusesAtTheCap(t *testing.T) {
	capture, err := newRequestCapture(config.Capture{Enabled: true, SampleRate: 1, MaxBytes: 512}, t.TempDir(), time.Now)
	if err != nil {
		t.Fatalf("newRequestCapture(): %v", err)
	}

	for range 20 {
		if err := capture.write(capturedRequest{Kind: "chat", RequestID: strings.Repeat("r", 64)}); err != nil {
			t.Fatalf("write(): %v", err)
		}
	}

	written, refused, failed, held := capture.stats()
	if failed != 0 {
		t.Fatalf("failed = %d, want 0: a writable directory must not report write failures", failed)
	}
	if refused == 0 {
		t.Fatal("the sink refused nothing at a 512-byte cap")
	}
	if written == 0 {
		t.Fatal("the sink wrote nothing at all")
	}
	if held > 512 {
		t.Fatalf("held = %d bytes, want no more than the 512-byte cap", held)
	}
}

// Test flow:
//  1. Create a `requestCapture` over a fresh directory.
//  2. Block its filter-rejected subdirectory path with a plain file instead of a directory.
//  3. Filter-reject one chat request through it.
//  4. Read back its stats.
//  5. Assert the write is counted as failed, not as written or refused, so an unwritable directory is distinguished from a capped one.
func TestCaptureCountsWritesItCouldNotMake(t *testing.T) {
	storageDir := t.TempDir()
	capture, err := newRequestCapture(config.Capture{Enabled: true, SampleRate: 1, MaxBytes: 1 << 20}, storageDir, time.Now)
	if err != nil {
		t.Fatalf("newRequestCapture(): %v", err)
	}
	blocker := filepath.Join(storageDir, captureDirName, captureFilterRejected)
	if err := os.MkdirAll(filepath.Dir(blocker), 0o755); err != nil {
		t.Fatalf("preparing the capture directory: %v", err)
	}
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("blocking the capture subdirectory: %v", err)
	}

	capture.filterRejected(chatRequest(t), "request-1", []byte(chatBody), engine.ErrStopped)

	written, refused, failed, _ := capture.stats()
	if failed != 1 {
		t.Fatalf("failed = %d, want 1: an unwritable directory must be counted", failed)
	}
	if written != 0 {
		t.Fatalf("written = %d, want 0: nothing reached the disk", written)
	}
	if refused != 0 {
		t.Fatalf("refused = %d, want 0: the cap was not the reason", refused)
	}
}
