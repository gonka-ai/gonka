package filters

import (
	"reflect"
	"strings"
	"testing"
)

// Test flow:
//  1. Build the expected map of role to messageRolePolicy.
//  2. Assert messageRolePolicies has the same number of entries.
//  3. Assert each expected role's policy matches exactly.
func TestMessageRolePoliciesTableIsComplete(t *testing.T) {
	want := map[string]messageRolePolicy{
		roleDeveloper: {disallowedFields: []string{"tool_calls", "tool_call_id", "function_call"}},
		roleSystem:    {disallowedFields: []string{"tool_calls", "tool_call_id", "function_call"}},
		roleUser:      {disallowedFields: []string{"tool_calls", "tool_call_id", "function_call"}},
		roleAssistant: {disallowedFields: []string{"tool_call_id"}},
		roleTool:      {disallowedFields: []string{"tool_calls", "function_call"}, requireToolCallID: true},
		roleFunction:  {disallowedFields: []string{"tool_calls", "tool_call_id", "function_call"}, requireName: true},
	}
	if len(messageRolePolicies) != len(want) {
		t.Fatalf("messageRolePolicies has %d roles, want %d", len(messageRolePolicies), len(want))
	}
	for role, wantPolicy := range want {
		gotPolicy, ok := messageRolePolicies[role]
		if !ok {
			t.Errorf("role %q missing from messageRolePolicies", role)
			continue
		}
		if !reflect.DeepEqual(gotPolicy, wantPolicy) {
			t.Errorf("messageRolePolicies[%q] = %+v, want %+v", role, gotPolicy, wantPolicy)
		}
	}
}

// Test flow:
//  1. Define message sequences covering a matched id, an unmatched id, a second reply to an already-consumed id, several distinct pending ids, a mix of matched and orphaned, and a non-map entry.
//  2. Run dropOrphanToolMessages on each sequence.
//  3. Assert no error, the reported changed flag, and the survivor roles in order.
func TestDropOrphanToolMessages(t *testing.T) {
	tests := []struct {
		name        string
		messages    []any
		wantDropped bool
		wantRoles   string
	}{
		{
			name: "matched id is kept",
			messages: []any{
				assistantWithToolCalls("c1"),
				toolMessage("c1", "result"),
			},
			wantDropped: false,
			wantRoles:   "assistant,tool",
		},
		{
			name: "unmatched id is dropped",
			messages: []any{
				map[string]any{"role": "user", "content": "hi"},
				toolMessage("nobody", "stray"),
				map[string]any{"role": "assistant", "content": "hello"},
			},
			wantDropped: true,
			wantRoles:   "user,assistant",
		},
		{
			name: "second tool reply to the same id is orphaned: the first consumed it",
			messages: []any{
				assistantWithToolCalls("c1"),
				toolMessage("c1", "first"),
				toolMessage("c1", "second"),
			},
			wantDropped: true,
			wantRoles:   "assistant,tool",
		},
		{
			name: "multiple distinct pending ids all match, in any order",
			messages: []any{
				assistantWithToolCalls("c1", "c2"),
				toolMessage("c2", "b"),
				toolMessage("c1", "a"),
			},
			wantDropped: false,
			wantRoles:   "assistant,tool,tool",
		},
		{
			name: "one matched and one orphaned among several tool messages",
			messages: []any{
				assistantWithToolCalls("c1"),
				toolMessage("c1", "ok"),
				toolMessage("c2", "orphan"),
			},
			wantDropped: true,
			wantRoles:   "assistant,tool",
		},
		{
			name: "non-map entry passes through untouched",
			messages: []any{
				"not-a-message",
				map[string]any{"role": "user", "content": "hi"},
			},
			wantDropped: false,
			wantRoles:   ",user",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			out, changed, err := dropOrphanToolMessages(testCase.messages)
			if err != nil {
				t.Fatalf("dropOrphanToolMessages() error = %v, want nil", err)
			}
			if changed != testCase.wantDropped {
				t.Errorf("changed = %v, want %v", changed, testCase.wantDropped)
			}
			if got := rolesOf(t, out); got != testCase.wantRoles {
				t.Errorf("survivor roles = %q, want %q", got, testCase.wantRoles)
			}
		})
	}
}

// Test flow:
//  1. Define single assistant messages varying content, tool_calls, and function_call between absent, nil, empty, and non-empty.
//  2. Run dropEmptyAssistantTurns on each one-message slice.
//  3. Assert no error, the reported changed flag, and the survivor count.
func TestDropEmptyAssistantTurns(t *testing.T) {
	tests := []struct {
		name              string
		message           map[string]any
		wantChanged       bool
		wantSurvivorCount int
	}{
		{"no fields at all", map[string]any{"role": "assistant"}, true, 0},
		{"empty string content", map[string]any{"role": "assistant", "content": ""}, true, 0},
		{"nil content", map[string]any{"role": "assistant", "content": nil}, true, 0},
		{"empty content parts array", map[string]any{"role": "assistant", "content": []any{}}, true, 0},
		{"empty tool_calls array", map[string]any{"role": "assistant", "tool_calls": []any{}}, true, 0},
		{"nil tool_calls", map[string]any{"role": "assistant", "tool_calls": nil}, true, 0},
		{"empty function_call object", map[string]any{"role": "assistant", "function_call": map[string]any{}}, true, 0},
		{"non-empty content is kept", map[string]any{"role": "assistant", "content": "answer"}, false, 1},
		{"non-empty tool_calls is kept", map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "c1"}}}, false, 1},
		{"non-empty function_call is kept", map[string]any{"role": "assistant", "function_call": map[string]any{"name": "fn"}}, false, 1},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			out, changed, err := dropEmptyAssistantTurns([]any{testCase.message})
			if err != nil {
				t.Fatalf("dropEmptyAssistantTurns() error = %v, want nil", err)
			}
			if changed != testCase.wantChanged {
				t.Errorf("changed = %v, want %v", changed, testCase.wantChanged)
			}
			if len(out) != testCase.wantSurvivorCount {
				t.Errorf("survivor count = %d, want %d", len(out), testCase.wantSurvivorCount)
			}
		})
	}
}

// Test flow:
//  1. Run dropEmptyAssistantTurns on a single user message with empty content.
//  2. Assert it is left unchanged as the sole survivor: empty user content is the validator's concern, not the normalizer's.
func TestDropEmptyAssistantTurnsLeavesOtherRolesAlone(t *testing.T) {
	out, changed, err := dropEmptyAssistantTurns([]any{map[string]any{"role": "user", "content": ""}})
	if err != nil {
		t.Fatalf("dropEmptyAssistantTurns() error = %v, want nil", err)
	}
	if changed || len(out) != 1 {
		t.Errorf("changed = %v, len(out) = %d, want unchanged single survivor", changed, len(out))
	}
}

// Test flow:
//  1. Run dropEmptyAssistantTurns on a slice containing one non-map entry.
//  2. Assert it passes through unchanged as the sole survivor.
func TestDropEmptyAssistantTurnsSkipsNonMapEntries(t *testing.T) {
	out, changed, err := dropEmptyAssistantTurns([]any{"not-a-message"})
	if err != nil {
		t.Fatalf("dropEmptyAssistantTurns() error = %v, want nil", err)
	}
	if changed || len(out) != 1 {
		t.Errorf("changed = %v, len(out) = %d, want the non-map entry passed through", changed, len(out))
	}
}

// Test flow:
//  1. Define tool messages with missing, nil, empty string, and empty array content.
//  2. Run normalizeEmptyMessageContent on each.
//  3. Assert changed is true and content becomes the emptyToolResultContent sentinel.
func TestNormalizeEmptyMessageContentFillsToolSentinel(t *testing.T) {
	tests := []struct {
		name    string
		message map[string]any
	}{
		{"missing content", map[string]any{"role": "tool", "tool_call_id": "c1"}},
		{"nil content", map[string]any{"role": "tool", "tool_call_id": "c1", "content": nil}},
		{"empty string content", map[string]any{"role": "tool", "tool_call_id": "c1", "content": ""}},
		{"empty content parts array", map[string]any{"role": "tool", "tool_call_id": "c1", "content": []any{}}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, changed, err := normalizeEmptyMessageContent([]any{testCase.message})
			if err != nil {
				t.Fatalf("normalizeEmptyMessageContent() error = %v, want nil", err)
			}
			if !changed {
				t.Fatal("changed = false, want true")
			}
			if testCase.message["content"] != emptyToolResultContent {
				t.Errorf("content = %v, want sentinel %q", testCase.message["content"], emptyToolResultContent)
			}
		})
	}
}

// Test flow:
//  1. Define assistant messages with empty string or empty array content alongside a tool_calls or function_call payload.
//  2. Run normalizeEmptyMessageContent on each.
//  3. Assert changed is true and content becomes nil.
func TestNormalizeEmptyMessageContentNullifiesAssistantWithCallPayload(t *testing.T) {
	tests := []struct {
		name    string
		message map[string]any
	}{
		{"empty string content with tool_calls", map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "c1"}}}},
		{"empty content parts array with tool_calls", map[string]any{"role": "assistant", "content": []any{}, "tool_calls": []any{map[string]any{"id": "c1"}}}},
		{"empty string content with function_call", map[string]any{"role": "assistant", "content": "", "function_call": map[string]any{"name": "fn"}}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, changed, err := normalizeEmptyMessageContent([]any{testCase.message})
			if err != nil {
				t.Fatalf("normalizeEmptyMessageContent() error = %v, want nil", err)
			}
			if !changed {
				t.Fatal("changed = false, want true")
			}
			if testCase.message["content"] != nil {
				t.Errorf("content = %v, want nil", testCase.message["content"])
			}
		})
	}
}

// Test flow:
//  1. Run normalizeEmptyMessageContent on an assistant message with empty content and no tool_calls or function_call.
//  2. Assert it is left unchanged: validateMessages rejects this case, so the normalizer must not paper over it by inventing content.
func TestNormalizeEmptyMessageContentLeavesAssistantWithoutCallsAlone(t *testing.T) {
	message := map[string]any{"role": "assistant", "content": ""}
	_, changed, err := normalizeEmptyMessageContent([]any{message})
	if err != nil {
		t.Fatalf("normalizeEmptyMessageContent() error = %v, want nil", err)
	}
	if changed || message["content"] != "" {
		t.Errorf("changed = %v, content = %v, want untouched empty string", changed, message["content"])
	}
}

// Test flow:
//  1. Run normalizeEmptyMessageContent on a user message with empty content.
//  2. Assert changed is false, since empty user content is the validator's concern.
func TestNormalizeEmptyMessageContentLeavesUserRoleAlone(t *testing.T) {
	message := map[string]any{"role": "user", "content": ""}
	_, changed, err := normalizeEmptyMessageContent([]any{message})
	if err != nil {
		t.Fatalf("normalizeEmptyMessageContent() error = %v, want nil", err)
	}
	if changed {
		t.Error("user content emptiness is the validator's concern, not the normalizer's")
	}
}

// Test flow:
//  1. Define tool, user, and assistant messages with and without a legacy name field.
//  2. Run stripLegacyToolName on each.
//  3. Assert the reported changed flag and whether the name field survives.
func TestStripLegacyToolName(t *testing.T) {
	tests := []struct {
		name        string
		message     map[string]any
		wantChanged bool
		wantHasName bool
	}{
		{"tool with name is stripped", map[string]any{"role": "tool", "tool_call_id": "c1", "content": "r", "name": "legacy"}, true, false},
		{"tool without name is a no-op", map[string]any{"role": "tool", "tool_call_id": "c1", "content": "r"}, false, false},
		{"user name is kept", map[string]any{"role": "user", "content": "hi", "name": "kept"}, false, true},
		{"assistant name is kept", map[string]any{"role": "assistant", "content": "hi", "name": "kept"}, false, true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, changed, err := stripLegacyToolName([]any{testCase.message})
			if err != nil {
				t.Fatalf("stripLegacyToolName() error = %v, want nil", err)
			}
			if changed != testCase.wantChanged {
				t.Errorf("changed = %v, want %v", changed, testCase.wantChanged)
			}
			_, hasName := testCase.message["name"]
			if hasName != testCase.wantHasName {
				t.Errorf("has name = %v, want %v", hasName, testCase.wantHasName)
			}
		})
	}
}

// Test flow:
//  1. Build a user message whose content is two text parts.
//  2. Run flattenMessageTextParts on it.
//  3. Assert changed is true and content becomes the parts joined with a newline.
func TestFlattenMessageTextPartsJoinsMultipleParts(t *testing.T) {
	message := map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "hello"},
		map[string]any{"type": "text", "text": "world"},
	}}
	_, changed, err := flattenMessageTextParts([]any{message})
	if err != nil {
		t.Fatalf("flattenMessageTextParts() error = %v, want nil", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if message["content"] != "hello\nworld" {
		t.Errorf("content = %v, want %q", message["content"], "hello\nworld")
	}
}

// Test flow:
//  1. Build a user message whose content is a single text part.
//  2. Run flattenMessageTextParts on it.
//  3. Assert changed is true and content becomes that part's text.
func TestFlattenMessageTextPartsSinglePart(t *testing.T) {
	message := map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "solo"}}}
	_, changed, err := flattenMessageTextParts([]any{message})
	if err != nil {
		t.Fatalf("flattenMessageTextParts() error = %v, want nil", err)
	}
	if !changed || message["content"] != "solo" {
		t.Errorf("changed = %v, content = %v, want (true, %q)", changed, message["content"], "solo")
	}
}

// Test flow:
//  1. Build a user message whose content is already a plain string.
//  2. Run flattenMessageTextParts on it.
//  3. Assert changed is false.
func TestFlattenMessageTextPartsLeavesStringContentAlone(t *testing.T) {
	message := map[string]any{"role": "user", "content": "already a string"}
	_, changed, err := flattenMessageTextParts([]any{message})
	if err != nil {
		t.Fatalf("flattenMessageTextParts() error = %v, want nil", err)
	}
	if changed {
		t.Error("changed = true, want false: string content must not trigger a rewrite")
	}
}

// Test flow:
//  1. Build a user message whose content is an empty array.
//  2. Run flattenMessageTextParts on it.
//  3. Assert changed is false: combineTextContentParts returns "" for an empty slice, and the flattener treats "" as nothing to write back, so an explicitly empty array survives unflattened.
func TestFlattenMessageTextPartsLeavesEmptyArrayAlone(t *testing.T) {
	message := map[string]any{"role": "user", "content": []any{}}
	_, changed, err := flattenMessageTextParts([]any{message})
	if err != nil {
		t.Fatalf("flattenMessageTextParts() error = %v, want nil", err)
	}
	if changed {
		t.Error("changed = true, want false: empty array must not become an empty string")
	}
}

// Test flow:
//  1. Build user messages with missing content and with nil content.
//  2. Run flattenMessageTextParts on each.
//  3. Assert changed is false for both.
func TestFlattenMessageTextPartsSkipsMissingOrNilContent(t *testing.T) {
	for _, message := range []map[string]any{
		{"role": "user"},
		{"role": "user", "content": nil},
	} {
		_, changed, err := flattenMessageTextParts([]any{message})
		if err != nil {
			t.Fatalf("flattenMessageTextParts() error = %v, want nil", err)
		}
		if changed {
			t.Errorf("changed = true for %v, want false", message)
		}
	}
}

// Test flow:
//  1. Run flattenMessageTextParts on a slice containing one non-map entry.
//  2. Assert changed is false.
func TestFlattenMessageTextPartsSkipsNonMapEntry(t *testing.T) {
	_, changed, err := flattenMessageTextParts([]any{"not-a-message"})
	if err != nil {
		t.Fatalf("flattenMessageTextParts() error = %v, want nil", err)
	}
	if changed {
		t.Error("changed = true, want false")
	}
}

// Test flow:
//  1. Build two messages where the second has a content part with an unsupported type.
//  2. Run flattenMessageTextParts on the slice.
//  3. Assert the error names the message index, part index, and the unsupported type.
func TestFlattenMessageTextPartsReportsMessageAndPartIndexOnNonTextPart(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "ok"},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "text": "x"}}},
	}
	_, _, err := flattenMessageTextParts(messages)
	want := `messages[1].content[0].type has unsupported value "image_url"`
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

// Test flow:
//  1. Parse a document with a user message and an orphan tool message whose content has an unsupported part type.
//  2. Run normalizeMessages on the document.
//  3. Assert it returns no error: the real chain order must silently drop the orphan tool message -- including its malformed content -- rather than surface a flatten error for a message on its way out.
//  4. Assert only the user message survives.
func TestMessageNormalizerChainDropsOrphanBeforeFlatteningContent(t *testing.T) {
	document := parseTestDocument(t, `{"messages":[
		{"role":"user","content":"q"},
		{"role":"tool","tool_call_id":"ghost","content":[{"type":"image_url","text":"x"}]}
	]}`)
	if err := normalizeMessages(document); err != nil {
		t.Fatalf("normalizeMessages() = %v, want nil (orphan drop must precede the flatten error)", err)
	}
	messages, _ := document.Array("messages")
	if len(messages) != 1 {
		t.Fatalf("want the orphan tool message dropped, got %d survivors: %v", len(messages), messages)
	}
}

// Test flow:
//  1. Build the same user-plus-orphan-tool-message pair as a raw slice.
//  2. Run flattenMessageTextParts directly on it, bypassing the orphan drop.
//  3. Assert it surfaces the unsupported-part-type error, proving that running the flatten step before the orphan drop instead of after changes the outcome.
func TestMessageNormalizerChainOrderIsObservable(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "q"},
		map[string]any{"role": "tool", "tool_call_id": "ghost", "content": []any{
			map[string]any{"type": "image_url", "text": "x"},
		}},
	}
	_, _, err := flattenMessageTextParts(messages)
	want := `messages[1].content[0].type has unsupported value "image_url"`
	if err == nil || err.Error() != want {
		t.Fatalf("flattenMessageTextParts() run before the orphan drop: error = %v, want %q", err, want)
	}
}

// Test flow:
//  1. Parse an empty document.
//  2. Run normalizeMessages on it.
//  3. Assert no error and that no messages field was created.
func TestNormalizeMessagesAbsentIsNoOp(t *testing.T) {
	document := parseTestDocument(t, `{}`)
	if err := normalizeMessages(document); err != nil {
		t.Fatalf("normalizeMessages() = %v, want nil", err)
	}
	if document.Has("messages") {
		t.Error("normalizeMessages must not create a messages field")
	}
}

// Test flow:
//  1. Parse a document with a single already-normalized user message.
//  2. Run normalizeMessages on it.
//  3. Assert no error and the message survives untouched.
func TestNormalizeMessagesNoOpWhenNothingToNormalize(t *testing.T) {
	document := parseTestDocument(t, `{"messages":[{"role":"user","content":"hi"}]}`)
	if err := normalizeMessages(document); err != nil {
		t.Fatalf("normalizeMessages() = %v, want nil", err)
	}
	messages, _ := document.Array("messages")
	if len(messages) != 1 {
		t.Fatalf("want 1 untouched message, got %d", len(messages))
	}
}

// Test flow:
//  1. Parse a document with a user message holding two text parts, an assistant message with a tool call, and a tool reply carrying a legacy name field.
//  2. Run normalizeMessages on the document in one pass.
//  3. Assert the user content is flattened into one newline-joined string.
//  4. Assert the tool message's legacy name field is stripped.
func TestNormalizeMessagesRunsTheFullChain(t *testing.T) {
	document := parseTestDocument(t, `{"messages":[
		{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"fn"}}]},
		{"role":"tool","tool_call_id":"c1","name":"legacy","content":"4"}
	]}`)
	if err := normalizeMessages(document); err != nil {
		t.Fatalf("normalizeMessages() = %v, want nil", err)
	}
	messages, _ := document.Array("messages")
	first := messages[0].(map[string]any)
	if first["content"] != "a\nb" {
		t.Errorf("messages[0].content = %v, want %q", first["content"], "a\nb")
	}
	last := messages[2].(map[string]any)
	if _, hasName := last["name"]; hasName {
		t.Error("messages[2].name must be stripped")
	}
}

// Test flow:
//  1. Parse a document whose message content has an unsupported part type.
//  2. Run normalizeMessages on the document.
//  3. Assert it returns a 400 rejection carrying the flatten error's message.
func TestNormalizeMessagesPropagatesFlattenErrorAsRejection(t *testing.T) {
	document := parseTestDocument(t, `{"messages":[{"role":"user","content":[{"type":"image_url","text":"x"}]}]}`)
	err := normalizeMessages(document)
	if err == nil {
		t.Fatal("normalizeMessages() = nil, want a rejection")
	}
	if got := ErrorStatus(err, 0); got != 400 {
		t.Errorf("ErrorStatus() = %d, want 400", got)
	}
	want := `messages[0].content[0].type has unsupported value "image_url"`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// Test flow:
//  1. Run validateMessages on a document with no messages field.
//  2. Assert it is rejected as required.
func TestValidateMessagesRequiredWhenAbsent(t *testing.T) {
	assertValidateMessagesRejects(t, `{}`, "messages is required")
}

// Test flow:
//  1. Run validateMessages on a document whose messages field is a string, not an array.
//  2. Assert it is rejected as required, the same as a missing field: Document.Array's type assertion fails the same way for the wrong type as for a missing key.
func TestValidateMessagesNonArrayTreatedAsAbsent(t *testing.T) {
	assertValidateMessagesRejects(t, `{"messages":"not-an-array"}`, "messages is required")
}

// Test flow:
//  1. Run validateMessages on a document with an empty messages array.
//  2. Assert it is rejected as must not be empty.
func TestValidateMessagesMustNotBeEmpty(t *testing.T) {
	assertValidateMessagesRejects(t, `{"messages":[]}`, "messages must not be empty")
}

// Test flow:
//  1. Run validateMessages on a document whose messages array holds a non-object entry.
//  2. Assert it is rejected for that entry not being an object.
func TestValidateMessagesRejectsNonObjectEntry(t *testing.T) {
	assertValidateMessagesRejects(t, `{"messages":["not-an-object"]}`, "messages[0] must be an object")
}

// Test flow:
//  1. Define messages with a missing role, a null role, a non-string role, and a whitespace-only role.
//  2. Run validateMessages on each.
//  3. Assert each is rejected with the matching role error.
func TestValidateMessagesRoleRequired(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantMessage string
	}{
		{"missing role", `{"messages":[{"content":"hi"}]}`, "messages[0].role: is required"},
		{"null role", `{"messages":[{"role":null,"content":"hi"}]}`, "messages[0].role: is required"},
		{"non-string role", `{"messages":[{"role":5,"content":"hi"}]}`, "messages[0].role: must be a string"},
		{"blank role", `{"messages":[{"role":"  ","content":"hi"}]}`, "messages[0].role: must not be empty"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			assertValidateMessagesRejects(t, testCase.body, testCase.wantMessage)
		})
	}
}

// Test flow:
//  1. Run validateMessages on a message with an unrecognized role value.
//  2. Assert it is rejected as an unsupported value.
func TestValidateMessagesRoleEnumRejectsUnknownValue(t *testing.T) {
	assertValidateMessagesRejects(t, `{"messages":[{"role":"pirate","content":"arr"}]}`, `messages[0].role has unsupported value "pirate"`)
}

// Test flow:
//  1. Parse a document with one message for each known role: developer, system, user, assistant, and function.
//  2. Run validateMessages on the document.
//  3. Assert no error.
func TestValidateMessagesAcceptsEveryKnownRole(t *testing.T) {
	document := parseTestDocument(t, `{"messages":[
		{"role":"developer","content":"a"},
		{"role":"system","content":"b"},
		{"role":"user","content":"c"},
		{"role":"assistant","content":"d"},
		{"role":"function","name":"fn","content":"e"}
	]}`)
	if err := validateMessages(document); err != nil {
		t.Fatalf("validateMessages() = %v, want nil", err)
	}
}

// Test flow:
//  1. Define messages pairing each role with a field its policy disallows: tool_calls, tool_call_id, or function_call.
//  2. Run validateMessages on each.
//  3. Assert each is rejected naming the disallowed field for that role.
func TestValidateMessagesRejectsDisallowedFieldPerRole(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantMessage string
	}{
		{"developer/tool_calls", `{"messages":[{"role":"developer","content":"hi","tool_calls":[]}]}`, "messages[0]: tool_calls is not allowed for this role"},
		{"developer/tool_call_id", `{"messages":[{"role":"developer","content":"hi","tool_call_id":"x"}]}`, "messages[0]: tool_call_id is not allowed for this role"},
		{"developer/function_call", `{"messages":[{"role":"developer","content":"hi","function_call":{}}]}`, "messages[0]: function_call is not allowed for this role"},
		{"system/tool_calls", `{"messages":[{"role":"system","content":"hi","tool_calls":[]}]}`, "messages[0]: tool_calls is not allowed for this role"},
		{"system/tool_call_id", `{"messages":[{"role":"system","content":"hi","tool_call_id":"x"}]}`, "messages[0]: tool_call_id is not allowed for this role"},
		{"system/function_call", `{"messages":[{"role":"system","content":"hi","function_call":{}}]}`, "messages[0]: function_call is not allowed for this role"},
		{"user/tool_calls", `{"messages":[{"role":"user","content":"hi","tool_calls":[]}]}`, "messages[0]: tool_calls is not allowed for this role"},
		{"user/tool_call_id", `{"messages":[{"role":"user","content":"hi","tool_call_id":"x"}]}`, "messages[0]: tool_call_id is not allowed for this role"},
		{"user/function_call", `{"messages":[{"role":"user","content":"hi","function_call":{}}]}`, "messages[0]: function_call is not allowed for this role"},
		{"assistant/tool_call_id", `{"messages":[{"role":"assistant","content":"hi","tool_call_id":"x"}]}`, "messages[0]: tool_call_id is not allowed for this role"},
		{"tool/tool_calls", `{"messages":[{"role":"tool","tool_call_id":"c1","content":"hi","tool_calls":[]}]}`, "messages[0]: tool_calls is not allowed for this role"},
		{"tool/function_call", `{"messages":[{"role":"tool","tool_call_id":"c1","content":"hi","function_call":{}}]}`, "messages[0]: function_call is not allowed for this role"},
		{"function/tool_calls", `{"messages":[{"role":"function","name":"fn","content":"hi","tool_calls":[]}]}`, "messages[0]: tool_calls is not allowed for this role"},
		{"function/tool_call_id", `{"messages":[{"role":"function","name":"fn","content":"hi","tool_call_id":"x"}]}`, "messages[0]: tool_call_id is not allowed for this role"},
		{"function/function_call", `{"messages":[{"role":"function","name":"fn","content":"hi","function_call":{}}]}`, "messages[0]: function_call is not allowed for this role"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			assertValidateMessagesRejects(t, testCase.body, testCase.wantMessage)
		})
	}
}

// Test flow:
//  1. Define developer, system, and user messages with missing, null, or empty content.
//  2. Run validateMessages on each.
//  3. Assert each is rejected with the matching content error.
func TestValidateMessagesRequiresContentForSimpleRoles(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantMessage string
	}{
		{"developer missing content", `{"messages":[{"role":"developer"}]}`, "messages[0].content: is required"},
		{"developer null content", `{"messages":[{"role":"developer","content":null}]}`, "messages[0].content: is required"},
		{"system empty content", `{"messages":[{"role":"system","content":""}]}`, "messages[0].content: must not be empty"},
		{"user missing content", `{"messages":[{"role":"user"}]}`, "messages[0].content: is required"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			assertValidateMessagesRejects(t, testCase.body, testCase.wantMessage)
		})
	}
}

// Test flow:
//  1. Run validateMessages on a tool message that has content but no tool_call_id.
//  2. Assert it is rejected for the missing tool_call_id, ahead of any content check.
func TestValidateMessagesToolRequiresToolCallIDBeforeContent(t *testing.T) {
	assertValidateMessagesRejects(t, `{"messages":[{"role":"tool","content":"result"}]}`, "messages[0].tool_call_id: is required")
}

// Test flow:
//  1. Parse a document with an assistant tool call followed by a tool reply that matches its id but carries no content.
//  2. Run validateMessages on the document.
//  3. Assert it is rejected for the missing content, after the id match succeeds.
func TestValidateMessagesToolContentRequiredAfterMatch(t *testing.T) {
	assertValidateMessagesRejects(t, `{"messages":[
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"fn"}}]},
		{"role":"tool","tool_call_id":"c1"}
	]}`, "messages[1].content: is required")
}

// Test flow:
//  1. Run validateMessages on a function-role message with content but no name.
//  2. Assert it is rejected for the missing name.
func TestValidateMessagesFunctionRequiresName(t *testing.T) {
	assertValidateMessagesRejects(t, `{"messages":[{"role":"function","content":"result"}]}`, "messages[0].name: is required")
}

// Test flow:
//  1. Run validateMessages on a function-role message with a name but no content.
//  2. Assert it is rejected for the missing content.
func TestValidateMessagesFunctionContentRequiredAfterName(t *testing.T) {
	assertValidateMessagesRejects(t, `{"messages":[{"role":"function","name":"fn"}]}`, "messages[0].content: is required")
}

// Test flow:
//  1. Define assistant messages varying content presence together with tool_calls or function_call presence.
//  2. Run validateMessages on each.
//  3. Assert acceptance or rejection matches the expected content rule for that combination.
func TestValidateMessagesAssistantContentRules(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantMessage string
	}{
		{
			"content required without tool_calls or function_call",
			`{"messages":[{"role":"assistant"}]}`,
			"messages[0].content: is required unless tool_calls or function_call is provided",
		},
		{
			"content omitted is fine when tool_calls is present",
			`{"messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"fn"}}]}]}`,
			"",
		},
		{
			"null content is fine when function_call is present",
			`{"messages":[{"role":"assistant","content":null,"function_call":{"name":"fn"}}]}`,
			"",
		},
		{
			"present empty content is rejected even with tool_calls present",
			`{"messages":[{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"fn"}}]}]}`,
			"messages[0].content: must not be empty",
		},
		{
			"plain content without any call payload is accepted",
			`{"messages":[{"role":"assistant","content":"hi"}]}`,
			"",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			err := validateMessages(document)
			if testCase.wantMessage == "" {
				if err != nil {
					t.Fatalf("validateMessages() = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != testCase.wantMessage {
				t.Errorf("validateMessages() error = %v, want %q", err, testCase.wantMessage)
			}
		})
	}
}

// Test flow:
//  1. Define assistant messages with malformed tool_calls: wrong type, empty array, non-object element, missing or duplicate id, wrong call type, and a malformed function field.
//  2. Run validateMessages on each.
//  3. Assert each is rejected with the matching shape error.
func TestValidateMessagesAssistantToolCallsShapeErrors(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantMessage string
	}{
		{"not an array", `{"messages":[{"role":"assistant","content":null,"tool_calls":"nope"}]}`, "messages[0].tool_calls must be an array"},
		{"empty array", `{"messages":[{"role":"assistant","content":null,"tool_calls":[]}]}`, "messages[0].tool_calls must not be empty"},
		{"element not an object", `{"messages":[{"role":"assistant","content":null,"tool_calls":["x"]}]}`, "messages[0].tool_calls[0] must be an object"},
		{"missing id", `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"type":"function","function":{"name":"fn"}}]}]}`, "messages[0].tool_calls[0].id: is required"},
		{"duplicate id", `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"x","type":"function","function":{"name":"fn"}},{"id":"x","type":"function","function":{"name":"fn"}}]}]}`, "messages[0].tool_calls[1].id is duplicated"},
		{"wrong type", `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"x","type":"code_interpreter","function":{"name":"fn"}}]}]}`, `messages[0].tool_calls[0].type must be "function"`},
		{"function not an object", `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"x","type":"function","function":"nope"}]}]}`, "messages[0].tool_calls[0].function must be an object"},
		{"function missing name", `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"x","type":"function","function":{}}]}]}`, "messages[0].tool_calls[0].function.name: is required"},
		{"function arguments not a string", `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"x","type":"function","function":{"name":"fn","arguments":42}}]}]}`, "messages[0].tool_calls[0].function.arguments: must be a string"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			assertValidateMessagesRejects(t, testCase.body, testCase.wantMessage)
		})
	}
}

// Test flow:
//  1. Define assistant messages with malformed function_call: not an object, missing name, and non-string arguments.
//  2. Run validateMessages on each.
//  3. Assert each is rejected with the matching shape error.
func TestValidateMessagesAssistantFunctionCallShapeErrors(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantMessage string
	}{
		{"not an object", `{"messages":[{"role":"assistant","content":null,"function_call":"x"}]}`, "messages[0].function_call must be an object"},
		{"missing name", `{"messages":[{"role":"assistant","content":null,"function_call":{}}]}`, "messages[0].function_call.name: is required"},
		{"arguments not a string", `{"messages":[{"role":"assistant","content":null,"function_call":{"name":"fn","arguments":42}}]}`, "messages[0].function_call.arguments: must be a string"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			assertValidateMessagesRejects(t, testCase.body, testCase.wantMessage)
		})
	}
}

// Test flow:
//  1. Parse an assistant message with content plus explicit null tool_calls and null function_call.
//  2. Run validateMessages on the document.
//  3. Assert no error.
//  4. Assert both null fields are deleted from the message.
func TestValidateMessagesAssistantNullToolCallsAndFunctionCallTreatedAsAbsent(t *testing.T) {
	document := parseTestDocument(t, `{"messages":[{"role":"assistant","content":"hello","tool_calls":null,"function_call":null}]}`)
	if err := validateMessages(document); err != nil {
		t.Fatalf("validateMessages() = %v, want nil", err)
	}
	messages, _ := document.Array("messages")
	message := messages[0].(map[string]any)
	if _, has := message["tool_calls"]; has {
		t.Error("null tool_calls must be deleted from the message")
	}
	if _, has := message["function_call"]; has {
		t.Error("null function_call must be deleted from the message")
	}
}

// Test flow:
//  1. Run validateMessages directly, without normalizeMessages first, on a tool message whose tool_call_id has no pending assistant call.
//  2. Assert it is rejected for not matching any previous tool_calls -- reachable only because the real pipeline's orphan-tool-message dropper would remove this case earlier.
func TestValidateMessagesToolCallIDMustMatchPendingAssistantCall(t *testing.T) {
	assertValidateMessagesRejects(t, `{"messages":[{"role":"tool","tool_call_id":"ghost","content":"x"}]}`,
		"messages[0].tool_call_id does not match any previous assistant tool_calls")
}

// Test flow:
//  1. Parse a document with a user message, an assistant tool call, and a tool reply that matches the call's id.
//  2. Run validateMessages on the document.
//  3. Assert no error.
func TestValidateMessagesToolCallIDMatchesAndConsumesPending(t *testing.T) {
	document := parseTestDocument(t, `{"messages":[
		{"role":"user","content":"q"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"fn"}}]},
		{"role":"tool","tool_call_id":"c1","content":"result"}
	]}`)
	if err := validateMessages(document); err != nil {
		t.Fatalf("validateMessages() = %v, want nil", err)
	}
}

// Test flow:
//  1. Parse a document with one assistant tool call answered by two tool replies sharing the same id.
//  2. Run validateMessages on the document.
//  3. Assert the second reply is rejected for not matching any previous tool_calls, since the first reply already consumed the pending call.
func TestValidateMessagesToolCallIDConsumedOnlyOnce(t *testing.T) {
	assertValidateMessagesRejects(t, `{"messages":[
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"fn"}}]},
		{"role":"tool","tool_call_id":"c1","content":"first"},
		{"role":"tool","tool_call_id":"c1","content":"second"}
	]}`, "messages[2].tool_call_id does not match any previous assistant tool_calls")
}

// Test flow:
//  1. Define content values covering empty and whitespace strings, empty and non-empty arrays, nil, a number, and an object.
//  2. Call isEmptyContent on each value.
//  3. Assert the result matches the expected emptiness.
func TestIsEmptyContent(t *testing.T) {
	tests := []struct {
		name    string
		content any
		want    bool
	}{
		{"empty string", "", true},
		{"whitespace string", "   ", true},
		{"non-empty string", "x", false},
		{"empty array", []any{}, true},
		{"non-empty array", []any{1}, false},
		{"nil is absent, not empty", nil, false},
		{"number is not empty", 42, false},
		{"object is not empty", map[string]any{}, false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isEmptyContent(testCase.content); got != testCase.want {
				t.Errorf("isEmptyContent(%#v) = %v, want %v", testCase.content, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Define assistant messages varying content, tool_calls, and function_call between absent, nil, empty, and non-empty.
//  2. Call isAssistantTurnEmpty on each message.
//  3. Assert the result matches the expected emptiness.
func TestIsAssistantTurnEmpty(t *testing.T) {
	tests := []struct {
		name    string
		message map[string]any
		want    bool
	}{
		{"no fields", map[string]any{}, true},
		{"has content", map[string]any{"content": "hi"}, false},
		{"non-empty tool_calls", map[string]any{"tool_calls": []any{map[string]any{"id": "x"}}}, false},
		{"empty tool_calls array", map[string]any{"tool_calls": []any{}}, true},
		{"nil tool_calls", map[string]any{"tool_calls": nil}, true},
		{"non-empty function_call", map[string]any{"function_call": map[string]any{"name": "fn"}}, false},
		{"empty function_call object", map[string]any{"function_call": map[string]any{}}, true},
		{"nil content", map[string]any{"content": nil}, true},
		{"empty content array", map[string]any{"content": []any{}}, true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isAssistantTurnEmpty(testCase.message); got != testCase.want {
				t.Errorf("isAssistantTurnEmpty(%#v) = %v, want %v", testCase.message, got, testCase.want)
			}
		})
	}
}

func assistantWithToolCalls(ids ...string) map[string]any {
	calls := make([]any, 0, len(ids))
	for _, id := range ids {
		calls = append(calls, map[string]any{"id": id, "type": "function", "function": map[string]any{"name": "fn"}})
	}
	return map[string]any{"role": "assistant", "content": "", "tool_calls": calls}
}

func toolMessage(toolCallID, content string) map[string]any {
	return map[string]any{"role": "tool", "tool_call_id": toolCallID, "content": content}
}

// rolesOf joins the role of every map entry in messages with ",", using "" for non-message entries.
func rolesOf(t *testing.T, messages []any) string {
	t.Helper()
	roles := make([]string, len(messages))
	for i, raw := range messages {
		if message, ok := raw.(map[string]any); ok {
			roles[i], _ = message["role"].(string)
		}
	}
	return strings.Join(roles, ",")
}

func assertValidateMessagesRejects(t *testing.T, body, wantMessage string) {
	t.Helper()
	document := parseTestDocument(t, body)
	err := validateMessages(document)
	if err == nil || err.Error() != wantMessage {
		t.Errorf("validateMessages() error = %v, want %q", err, wantMessage)
	}
}
