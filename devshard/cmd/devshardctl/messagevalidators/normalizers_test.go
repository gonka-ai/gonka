package messagevalidators

import (
	"strings"
	"testing"

	"devshard/cmd/devshardctl/testutil"
)

// ============================================================
// OrphanToolMessageDropper
// ============================================================

func TestOrphanToolMessageDropper_DropsToolWithUnmatchedID(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "hi"},
		map[string]any{"role": "tool", "tool_call_id": "nobody", "content": "stray"},
		map[string]any{"role": "assistant", "content": "hello"},
	}
	out, changed, err := OrphanToolMessageDropper{}.Apply(msgs)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if !changed {
		t.Fatal("changed must be true when something is dropped")
	}
	if len(out) != 2 {
		t.Fatalf("want 2 surviving messages, got %d", len(out))
	}
	if testutil.MapAt(t, out, 1)["role"] != "assistant" {
		t.Fatal("orphan tool message must be dropped, assistant survives")
	}
}

func TestOrphanToolMessageDropper_KeepsMatchedToolMessage(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "q"},
		map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "fn"}},
		}},
		map[string]any{"role": "tool", "tool_call_id": "c1", "content": "result"},
	}
	out, changed, err := OrphanToolMessageDropper{}.Apply(msgs)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if changed {
		t.Fatal("nothing should change for a well-formed sequence")
	}
	if len(out) != 3 {
		t.Fatalf("want 3 messages, got %d", len(out))
	}
}

func TestOrphanToolMessageDropper_ConsumesPendingOnMatch(t *testing.T) {
	// Same id appearing twice — second tool message is orphan because
	// the first consumed the pending entry.
	msgs := []any{
		map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "fn"}},
		}},
		map[string]any{"role": "tool", "tool_call_id": "c1", "content": "first"},
		map[string]any{"role": "tool", "tool_call_id": "c1", "content": "second"},
	}
	out, changed, _ := OrphanToolMessageDropper{}.Apply(msgs)
	if !changed {
		t.Fatal("must drop the second tool message")
	}
	if len(out) != 2 {
		t.Fatalf("want 2 survivors, got %d", len(out))
	}
}

// ============================================================
// EmptyAssistantTurnDropper
// ============================================================

func TestEmptyAssistantTurnDropper(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "hi"},
		map[string]any{"role": "assistant"},                      // truly empty
		map[string]any{"role": "assistant", "content": ""},       // empty string content
		map[string]any{"role": "assistant", "content": "answer"}, // keep
		map[string]any{"role": "user", "content": ""},            // user role NOT dropped here (validator job)
		map[string]any{"role": "assistant", "tool_calls": []any{ // tool_calls makes it non-empty
			map[string]any{"id": "c1"},
		}},
	}
	out, changed, _ := EmptyAssistantTurnDropper{}.Apply(msgs)
	if !changed {
		t.Fatal("must drop 2 empty assistant turns")
	}
	if len(out) != 4 {
		t.Fatalf("want 4 survivors, got %d", len(out))
	}
	// Survivors: user[0], assistant[answer], user[empty], assistant[tool_calls]
	roles := []string{}
	for _, m := range out {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	got := strings.Join(roles, ",")
	if got != "user,assistant,user,assistant" {
		t.Fatalf("survivor roles: %q", got)
	}
}

// ============================================================
// EmptyContentNormalizer
// ============================================================

func TestEmptyContentNormalizer_FillsToolWithSentinel(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "tool", "tool_call_id": "c1"}, // missing content
		map[string]any{"role": "tool", "tool_call_id": "c2", "content": nil},
		map[string]any{"role": "tool", "tool_call_id": "c3", "content": ""},
	}
	_, changed, _ := EmptyContentNormalizer{ToolSentinel: "(no result)"}.Apply(msgs)
	if !changed {
		t.Fatal("must have rewritten all three tool messages")
	}
	for i, raw := range msgs {
		m := raw.(map[string]any)
		if m["content"] != "(no result)" {
			t.Fatalf("[%d] want sentinel, got %v", i, m["content"])
		}
	}
}

func TestEmptyContentNormalizer_NullifiesAssistantWithCalls(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "c1"}}},
		map[string]any{"role": "assistant", "content": "", "function_call": map[string]any{"name": "fn"}},
	}
	_, changed, _ := EmptyContentNormalizer{ToolSentinel: "x"}.Apply(msgs)
	if !changed {
		t.Fatal("must nullify content on assistant turns with call payload")
	}
	for i, raw := range msgs {
		m := raw.(map[string]any)
		if m["content"] != nil {
			t.Fatalf("[%d] want nil content, got %v", i, m["content"])
		}
	}
}

func TestEmptyContentNormalizer_LeavesAssistantWithoutCalls(t *testing.T) {
	// Empty content on assistant turn WITHOUT call payload is left untouched —
	// the validator will reject it as malformed. The normalizer must not paper
	// over the bug by inventing content.
	msg := map[string]any{"role": "assistant", "content": ""}
	msgs := []any{msg}
	_, changed, _ := EmptyContentNormalizer{ToolSentinel: "x"}.Apply(msgs)
	if changed {
		t.Fatal("must not touch assistant turn without call payload")
	}
	if msg["content"] != "" {
		t.Fatalf("content must remain empty string, got %v", msg["content"])
	}
}

func TestEmptyContentNormalizer_LeavesUserRoleAlone(t *testing.T) {
	// User role isn't a special case: empty content is the validator's
	// concern, not the normalizer's.
	msg := map[string]any{"role": "user", "content": ""}
	msgs := []any{msg}
	_, changed, _ := EmptyContentNormalizer{ToolSentinel: "x"}.Apply(msgs)
	if changed {
		t.Fatal("user role is not normalized")
	}
}

// ============================================================
// LegacyToolNameStripper
// ============================================================

func TestLegacyToolNameStripper(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "hi", "name": "kept"},                          // not tool — name kept
		map[string]any{"role": "tool", "tool_call_id": "c1", "content": "r", "name": "stripped"}, // tool — name stripped
		map[string]any{"role": "tool", "tool_call_id": "c2", "content": "r"},                     // no name — no-op
		map[string]any{"role": "assistant", "content": "x", "name": "assistant-name"},            // not tool — kept
	}
	_, changed, _ := LegacyToolNameStripper{}.Apply(msgs)
	if !changed {
		t.Fatal("must strip name from one tool message")
	}
	if _, has := testutil.MapAt(t, msgs, 0)["name"]; !has {
		t.Fatal("user.name must be preserved")
	}
	if _, has := testutil.MapAt(t, msgs, 1)["name"]; has {
		t.Fatal("tool.name must be stripped")
	}
	if _, has := testutil.MapAt(t, msgs, 3)["name"]; !has {
		t.Fatal("assistant.name must be preserved")
	}
}

// ============================================================
// TextPartsFlattener
// ============================================================

func TestTextPartsFlattener_JoinsTextParts(t *testing.T) {
	msg := map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "text", "text": "first"},
			map[string]any{"type": "text", "text": "second"},
		},
	}
	_, changed, err := TextPartsFlattener{}.Apply([]any{msg})
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if !changed {
		t.Fatal("must have flattened content")
	}
	if msg["content"] != "first\nsecond" {
		t.Fatalf("want 'first\\nsecond', got %v", msg["content"])
	}
}

func TestTextPartsFlattener_LeavesStringAlone(t *testing.T) {
	msg := map[string]any{"role": "user", "content": "already string"}
	_, changed, _ := TextPartsFlattener{}.Apply([]any{msg})
	if changed {
		t.Fatal("string content must not trigger change")
	}
}

func TestTextPartsFlattener_ErrorIncludesMessageIndex(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "ok"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "image_url", "text": "x"},
		}},
	}
	_, _, err := TextPartsFlattener{}.Apply(msgs)
	if err == nil {
		t.Fatal("want error from bad part")
	}
	if !strings.Contains(err.Error(), "messages[1].content") {
		t.Fatalf("error must include messages[1].content, got %v", err)
	}
}

func TestTextPartsFlattener_EmptyContentArrayLeftAlone(t *testing.T) {
	// CombineTextContentParts returns "" for empty arrays; flattener treats
	// "" as "no change" so the empty array is preserved (validator's call).
	msg := map[string]any{"role": "user", "content": []any{}}
	_, changed, _ := TextPartsFlattener{}.Apply([]any{msg})
	if changed {
		t.Fatal("empty content array must not be flattened to empty string")
	}
}
