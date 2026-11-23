package streamevidence

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Test flow:
//  1. Read the terminal-error SSE fixture `testdata/newlineless_final_error.sse`, whose only line is an error event with no final newline.
//  2. Take its `DataLines` and serialize them with `Payload`.
//  3. Assert the bytes equal `testdata/newlineless_final_error.json`.
func TestPayloadOfATerminalErrorStreamMatchesTheGolden(t *testing.T) {
	events, err := os.ReadFile(filepath.Join("testdata", "newlineless_final_error.sse"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "newlineless_final_error.json"))
	if err != nil {
		t.Fatalf("reading golden: %v", err)
	}

	payload, err := Payload(DataLines(events))
	if err != nil {
		t.Fatalf("Payload: %v", err)
	}

	if !bytes.Equal(payload, want) {
		t.Fatalf("Payload = %s, want %s", payload, want)
	}
}

// Test flow:
//  1. Serialize an error line whose message carries `<`, `>` and `&`, followed by `[DONE]`, with `Payload`.
//  2. Assert the bytes are the JSON object whose `events` array holds both lines in order, with quotes escaped and `<`, `>` and `&` written as `\u003c`, `\u003e` and `\u0026`.
func TestPayloadKeepsTheLinesVerbatimInOrder(t *testing.T) {
	payload, err := Payload([]string{`data: {"error":{"message":"<think> & boom"}}`, "data: [DONE]"})
	if err != nil {
		t.Fatalf("Payload: %v", err)
	}

	want := `{"events":["data: {\"error\":{\"message\":\"\u003cthink\u003e \u0026 boom\"}}","data: [DONE]"]}`
	if string(payload) != want {
		t.Fatalf("Payload = %s, want %s", payload, want)
	}
}

// Test flow:
//  1. List the error stream fixtures `testdata/*.sse` and assert the table below names each of them once.
//  2. For each table case, read the fixture, take the leading `DataLines` that fit in `MaxRetainedBytes` as a gateway retains them, and serialize them with `Payload`.
//  3. Assert `ProvesErrorMiss` of the payload matches whether a verifier reads an error in it.
//  4. Assert `ProvesErrorMiss` refuses an empty payload.
func TestProvesErrorMissOnlyForEvidenceThatCarriesAnError(t *testing.T) {
	testCases := []struct {
		fixture    string
		wantProves bool
	}{
		{fixture: "error_envelope_done.sse", wantProves: true},
		{fixture: "error_envelope_no_done.sse", wantProves: true},
		{fixture: "newlineless_final_error.sse", wantProves: true},
		{fixture: "content_then_error.sse", wantProves: true},
		{fixture: "object_error.sse", wantProves: true},
		{fixture: "error_without_space.sse", wantProves: false},
		{fixture: "error_as_string.sse", wantProves: false},
		{fixture: "long_error_stream.sse", wantProves: false},
	}
	paths, err := filepath.Glob(filepath.Join("testdata", "*.sse"))
	if err != nil {
		t.Fatalf("listing fixtures: %v", err)
	}
	fixtures := make([]string, 0, len(paths))
	for _, path := range paths {
		fixtures = append(fixtures, filepath.Base(path))
	}
	tableFixtures := make([]string, 0, len(testCases))
	for _, testCase := range testCases {
		tableFixtures = append(tableFixtures, testCase.fixture)
	}
	slices.Sort(tableFixtures)
	if !slices.Equal(fixtures, tableFixtures) {
		t.Fatalf("testdata holds %q, the table names %q", fixtures, tableFixtures)
	}

	for _, testCase := range testCases {
		events, err := os.ReadFile(filepath.Join("testdata", testCase.fixture))
		if err != nil {
			t.Fatalf("reading fixture: %v", err)
		}
		payload, err := Payload(retainedLines(DataLines(events)))
		if err != nil {
			t.Fatalf("Payload(%s): %v", testCase.fixture, err)
		}

		if proves := ProvesErrorMiss(payload); proves != testCase.wantProves {
			t.Errorf("ProvesErrorMiss(%s) = %v, want %v", testCase.fixture, proves, testCase.wantProves)
		}
	}
	if ProvesErrorMiss(nil) {
		t.Error("ProvesErrorMiss(nil) = true, want false: a verifier rejects an empty payload")
	}
}

func retainedLines(lines []string) []string {
	var kept []string
	heldBytes := 0
	for _, line := range lines {
		heldBytes += len(line) + 1
		if heldBytes > MaxRetainedBytes {
			break
		}
		kept = append(kept, line)
	}
	return kept
}
