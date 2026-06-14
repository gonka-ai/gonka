package messagevalidators

import (
	"errors"
	"strings"
	"testing"
)

func validatorForTest() MinimaxToolMessage {
	return MinimaxToolMessage{MaxEntries: 16, NameMaxLen: 64, TextMaxSize: 1024}
}

func TestMinimaxToolMessage_AcceptsCanonicalShape(t *testing.T) {
	v := validatorForTest()
	content := []any{
		map[string]any{"name": "get_weather", "type": "text", "text": `{"temperature":"25"}`},
		map[string]any{"name": "search_web", "type": "text", "text": "results"},
	}
	if err := v.Validate(content); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMinimaxToolMessage_RejectsNonArray(t *testing.T) {
	if err := validatorForTest().Validate("plain string"); !errors.Is(err, ErrMinimaxToolContentShape) {
		t.Fatalf("expected ErrMinimaxToolContentShape, got %v", err)
	}
}

func TestMinimaxToolMessage_RejectsEmptyArray(t *testing.T) {
	if err := validatorForTest().Validate([]any{}); !errors.Is(err, ErrMinimaxToolContentShape) {
		t.Fatalf("expected ErrMinimaxToolContentShape, got %v", err)
	}
}

func TestMinimaxToolMessage_RejectsTooManyEntries(t *testing.T) {
	v := MinimaxToolMessage{MaxEntries: 2, NameMaxLen: 64, TextMaxSize: 1024}
	content := []any{
		map[string]any{"name": "a", "type": "text", "text": "1"},
		map[string]any{"name": "b", "type": "text", "text": "2"},
		map[string]any{"name": "c", "type": "text", "text": "3"},
	}
	if err := v.Validate(content); !errors.Is(err, ErrMinimaxToolContentShape) {
		t.Fatalf("expected ErrMinimaxToolContentShape, got %v", err)
	}
}

func TestMinimaxToolMessage_RejectsNonObjectEntry(t *testing.T) {
	if err := validatorForTest().Validate([]any{"not an object"}); !errors.Is(err, ErrMinimaxToolEntryShape) {
		t.Fatalf("expected ErrMinimaxToolEntryShape, got %v", err)
	}
}

func TestMinimaxToolMessage_RejectsMissingName(t *testing.T) {
	content := []any{map[string]any{"type": "text", "text": "result"}}
	if err := validatorForTest().Validate(content); !errors.Is(err, ErrMinimaxToolEntryShape) {
		t.Fatalf("expected ErrMinimaxToolEntryShape, got %v", err)
	}
}

func TestMinimaxToolMessage_RejectsBlankName(t *testing.T) {
	content := []any{map[string]any{"name": "", "type": "text", "text": "result"}}
	if err := validatorForTest().Validate(content); !errors.Is(err, ErrMinimaxToolEntryShape) {
		t.Fatalf("expected ErrMinimaxToolEntryShape, got %v", err)
	}
}

// A whitespace-only name must be rejected too: it otherwise passes the gateway,
// reaches the MiniMax tool parser, and hangs the request until the deadline.
func TestMinimaxToolMessage_RejectsWhitespaceName(t *testing.T) {
	content := []any{map[string]any{"name": "   ", "type": "text", "text": "result"}}
	if err := validatorForTest().Validate(content); !errors.Is(err, ErrMinimaxToolEntryShape) {
		t.Fatalf("expected ErrMinimaxToolEntryShape, got %v", err)
	}
}

func TestMinimaxToolMessage_RejectsOverlongName(t *testing.T) {
	v := MinimaxToolMessage{MaxEntries: 16, NameMaxLen: 8, TextMaxSize: 1024}
	content := []any{map[string]any{"name": "way_too_long_function_name", "type": "text", "text": "ok"}}
	if err := v.Validate(content); !errors.Is(err, ErrMinimaxToolEntryShape) {
		t.Fatalf("expected ErrMinimaxToolEntryShape, got %v", err)
	}
}

func TestMinimaxToolMessage_RejectsWrongType(t *testing.T) {
	content := []any{map[string]any{"name": "fn", "type": "image_url", "text": "x"}}
	if err := validatorForTest().Validate(content); !errors.Is(err, ErrMinimaxToolEntryShape) {
		t.Fatalf("expected ErrMinimaxToolEntryShape, got %v", err)
	}
}

func TestMinimaxToolMessage_RejectsNonStringText(t *testing.T) {
	content := []any{map[string]any{"name": "fn", "type": "text", "text": 42}}
	if err := validatorForTest().Validate(content); !errors.Is(err, ErrMinimaxToolEntryShape) {
		t.Fatalf("expected ErrMinimaxToolEntryShape, got %v", err)
	}
}

func TestMinimaxToolMessage_RejectsOverlongText(t *testing.T) {
	v := MinimaxToolMessage{MaxEntries: 16, NameMaxLen: 64, TextMaxSize: 4}
	content := []any{map[string]any{"name": "fn", "type": "text", "text": strings.Repeat("a", 5)}}
	if err := v.Validate(content); !errors.Is(err, ErrMinimaxToolEntryShape) {
		t.Fatalf("expected ErrMinimaxToolEntryShape, got %v", err)
	}
}

func TestMinimaxToolMessage_RejectsExtraKeys(t *testing.T) {
	content := []any{map[string]any{"name": "fn", "type": "text", "text": "ok", "extra": true}}
	if err := validatorForTest().Validate(content); !errors.Is(err, ErrMinimaxToolEntryShape) {
		t.Fatalf("expected ErrMinimaxToolEntryShape, got %v", err)
	}
}
