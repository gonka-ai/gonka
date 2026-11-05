package host

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"common/completionapi"

	"devshard"
	"devshard/observability"
	"devshard/types"
)

// positionSlack is how far an honest claim may run past the positions its
// stored body keeps: the recorded builds claim exactly their positions.
const positionSlack = 3

// utf8TailPositions is the most trailing positions of a cut answer that may
// carry no text: the bytes of a character the cut left unfinished, which
// the engine counts and never serves.
const utf8TailPositions = 3

// servedAnswer is choice 0 of a stored body as the client was served it.
type servedAnswer struct {
	content   string
	reasoning string
	calls     []servedCall
	finish    string
	// positions are the generated positions (logprobs.content entries).
	positions uint64
	// trailingEmpty counts the positions at the end that carried no text.
	trailingEmpty uint64
}

type servedCall struct {
	id, name, arguments string
}

type storedChunk struct {
	Choices []struct {
		Index        int                 `json:"index"`
		Delta        *storedMessage      `json:"delta"`
		Message      *storedMessage      `json:"message"`
		Logprobs     *storedChoiceLogits `json:"logprobs"`
		FinishReason *string             `json:"finish_reason"`
	} `json:"choices"`
}

type storedMessage struct {
	Content          *string `json:"content"`
	Reasoning        *string `json:"reasoning"`
	ReasoningContent *string `json:"reasoning_content"`
	ToolCalls        []struct {
		Index    *int   `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

type storedChoiceLogits struct {
	Content []struct {
		Token string `json:"token"`
	} `json:"content"`
}

// parseServedAnswer reads choice 0 of a stored body, streamed envelope or
// plain completion. Reasoning comes under either key; one delta carrying
// both holds the same text twice and is read once. A streamed answer is read
// as the gateway counts it: from each delta, skipping an event that is not a
// readable chunk, since a streaming client reads neither a message nor such
// an event.
func parseServedAnswer(stored []byte) (servedAnswer, error) {
	var envelope struct {
		Events []string `json:"events"`
	}
	if err := json.Unmarshal(stored, &envelope); err != nil {
		return servedAnswer{}, err
	}
	var chunks [][]byte
	streamed := envelope.Events != nil
	if streamed {
		for _, event := range envelope.Events {
			data := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(event), "data:"))
			if data == "" || data == "[DONE]" {
				continue
			}
			chunks = append(chunks, []byte(data))
		}
	} else {
		chunks = [][]byte{stored}
	}
	var answer servedAnswer
	parts := answerParts{calls: map[int]*callParts{}}
	for _, raw := range chunks {
		var chunk storedChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			if streamed {
				continue
			}
			return servedAnswer{}, err
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			message := choice.Delta
			if !streamed {
				message = choice.Message
			}
			text := 0
			if message != nil {
				text = parts.add(message)
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				answer.finish = *choice.FinishReason
			}
			if choice.Logprobs == nil {
				continue
			}
			n := uint64(len(choice.Logprobs.Content))
			answer.positions += n
			if text > 0 {
				answer.trailingEmpty = 0
			} else {
				answer.trailingEmpty += n
			}
		}
	}
	answer.content, answer.reasoning = parts.content.String(), parts.reasoning.String()
	for _, call := range parts.order {
		answer.calls = append(answer.calls, servedCall{id: call.id, name: call.name.String(), arguments: call.arguments.String()})
	}
	return answer, nil
}

// answerParts gathers the text of a streamed answer. A body of one token per
// chunk runs to a million chunks, so the text is built, not concatenated.
type answerParts struct {
	content, reasoning strings.Builder
	calls              map[int]*callParts
	order              []*callParts
}

type callParts struct {
	id              string
	name, arguments strings.Builder
}

// add appends a delta or message to the answer and returns its text bytes.
// Reasoning is read as the gateway counts it: the reasoning key when it
// carries text, else reasoning_content.
func (p *answerParts) add(m *storedMessage) int {
	text := 0
	if m.Content != nil {
		p.content.WriteString(*m.Content)
		text += len(*m.Content)
	}
	reasoning := m.Reasoning
	if reasoning == nil || *reasoning == "" {
		reasoning = m.ReasoningContent
	}
	if reasoning != nil {
		p.reasoning.WriteString(*reasoning)
		text += len(*reasoning)
	}
	for i, call := range m.ToolCalls {
		index := i
		if call.Index != nil {
			index = *call.Index
		}
		parts, ok := p.calls[index]
		if !ok {
			parts = &callParts{}
			p.calls[index] = parts
			p.order = append(p.order, parts)
		}
		if call.ID != "" {
			parts.id = call.ID
		}
		parts.name.WriteString(call.Function.Name)
		parts.arguments.WriteString(call.Function.Arguments)
		text += len(call.Function.Name) + len(call.Function.Arguments)
	}
	return text
}

// turn is the answer as the assistant message a renderer appends to the
// request: the served text, and each call with the arguments it was served.
func (a servedAnswer) turn() map[string]any {
	turn := map[string]any{"role": "assistant", "content": a.content}
	if a.reasoning != "" {
		turn["reasoning"] = a.reasoning
	}
	if len(a.calls) > 0 {
		calls := make([]any, 0, len(a.calls))
		for i, call := range a.calls {
			id := call.id
			if id == "" {
				id = "call_" + strconv.Itoa(i)
			}
			calls = append(calls, map[string]any{
				"id":       id,
				"type":     "function",
				"function": map[string]any{"name": call.name, "arguments": call.arguments},
			})
		}
		turn["tool_calls"] = calls
	}
	return turn
}

// wholeArguments reports arguments that are a whole JSON object. The tool
// parsers of the models build the arguments they stream from a parsed object,
// so only a cut leaves them partial.
func wholeArguments(arguments string) bool {
	var parsed map[string]any
	return json.Unmarshal([]byte(arguments), &parsed) == nil && parsed != nil
}

// cutCall returns the last tool call when its arguments are not a whole JSON
// object: the answer was cut inside it.
func (a servedAnswer) cutCall() (servedCall, bool) {
	if len(a.calls) == 0 || wholeArguments(a.calls[len(a.calls)-1].arguments) {
		return servedCall{}, false
	}
	return a.calls[len(a.calls)-1], true
}

// brokenEarlierCall reports a tool call before the last one whose arguments
// are not a whole JSON object. A cut ends an answer, so an honest executor
// cannot serve one: the body was written by hand.
func (a servedAnswer) brokenEarlierCall() bool {
	for _, call := range a.calls[:max(len(a.calls), 1)-1] {
		if !wholeArguments(call.arguments) {
			return true
		}
	}
	return false
}

// openArgument returns the top-level key whose value the arguments of a cut
// call were writing, ok false when the cut fell outside one (in a key, or in
// a nested value the scan cannot place).
func openArgument(arguments string) (key string, ok bool) {
	dec := json.NewDecoder(strings.NewReader(arguments))
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		return "", false
	}
	for {
		token, err := dec.Token()
		if err != nil {
			return "", false
		}
		name, isKey := token.(string)
		if !isKey {
			return "", false
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return name, true
		}
	}
}

// stringArgument reports whether the request's tool schema types the named
// argument of the named function as a string, alone or with null.
func (t executedTerms) stringArgument(function, argument string) bool {
	switch typed := t.argumentTypes[function][argument].(type) {
	case string:
		return typed == "string"
	case []any:
		found := false
		for _, entry := range typed {
			switch entry {
			case "string":
				found = true
			case "null":
			default:
				return false
			}
		}
		return found
	}
	return false
}

// executedTerms are what the counts read of the request the executor's ML
// node ran: its max_tokens and min_tokens, its longest stop string, whether
// it is text only, whether it offers tools and the schema types of their
// arguments, and whether a grammar constrains it. The request carries no
// logit_bias or bad_words: the executor drops them before running it.
type executedTerms struct {
	maxTokens uint64
	tools     bool
	// argumentTypes are the schema "type" of each top-level argument, by
	// function name.
	argumentTypes map[string]map[string]any
	minTokens     uint64
	stopBytes     uint64
	textOnly      bool
	constrained   bool
}

func readExecutedTerms(request []byte) (executedTerms, error) {
	var body map[string]any
	if err := json.Unmarshal(request, &body); err != nil {
		return executedTerms{}, err
	}
	if body == nil {
		return executedTerms{}, errors.New("request is not a JSON object")
	}
	terms := executedTerms{textOnly: true, constrained: completionapi.GrammarConstrained(body)}
	if maxTokens, ok := body["max_tokens"].(float64); ok && maxTokens > 0 {
		terms.maxTokens = uint64(maxTokens)
	}
	if minTokens, ok := body["min_tokens"].(float64); ok && minTokens > 0 {
		terms.minTokens = uint64(minTokens)
	}
	tools, _ := body["tools"].([]any)
	terms.tools = len(tools) > 0
	terms.argumentTypes = map[string]map[string]any{}
	for _, tool := range tools {
		function, _ := asMap(tool)["function"].(map[string]any)
		name, _ := function["name"].(string)
		properties, _ := asMap(function["parameters"])["properties"].(map[string]any)
		types := map[string]any{}
		for argument, schema := range properties {
			types[argument] = asMap(schema)["type"]
		}
		terms.argumentTypes[name] = types
	}
	switch stop := body["stop"].(type) {
	case string:
		terms.stopBytes = uint64(len(stop))
	case []any:
		for _, s := range stop {
			if text, ok := s.(string); ok {
				terms.stopBytes = max(terms.stopBytes, uint64(len(text)))
			}
		}
	}
	messages, _ := body["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if !textContent(message["content"]) {
			terms.textOnly = false
		}
	}
	return terms, nil
}

// withThinking names the model's thinking mode in a request that does not
// name it. An answer that carried reasoning was generated in that mode, and a
// voter's build may default to the other one: DeepSeek-V4 on 3.0.16 renders a
// turn in chat mode and drops its reasoning, a third of an honest claim on the
// recorded answers of 3.1.0 and 4.0.0.
func withThinking(request []byte, kwarg string) ([]byte, bool, error) {
	var body map[string]any
	if err := json.Unmarshal(request, &body); err != nil {
		return nil, false, err
	}
	if body == nil {
		return nil, false, errors.New("request is not a JSON object")
	}
	kwargs, isObject := body["chat_template_kwargs"].(map[string]any)
	if body["chat_template_kwargs"] != nil && !isObject {
		return request, false, nil
	}
	if _, named := kwargs[kwarg]; named {
		return request, false, nil
	}
	if kwargs == nil {
		kwargs = map[string]any{}
	}
	kwargs[kwarg] = true
	body["chat_template_kwargs"] = kwargs
	request, err := json.Marshal(body)
	return request, err == nil, err
}

// textContent reports a message content that is a string, null, or a list
// of text parts; an image or audio part costs tokens its bytes do not show.
func textContent(content any) bool {
	switch content := content.(type) {
	case nil, string:
		return true
	case []any:
		for _, raw := range content {
			part, _ := raw.(map[string]any)
			if part["type"] != "text" {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// usageCount is what a voter counted for a challenged answer, logged with
// its vote.
type usageCount struct {
	positions uint64
	// excluded are positions the output count leaves out of the claim: the
	// unfinished UTF-8 tail of a cut answer.
	excluded      uint64
	output        uint64
	outputLimit   uint64
	outputSkipped string
	input         uint64
	inputLimit    uint64
	inputSkipped  string
	// outputRender and inputRender name where each count was made: the
	// host's ML node or a render server's URL.
	outputRender      string
	inputRender       string
	outputRenderNodes []string
	inputRenderNodes  []string
	// thinkingNamed marks a request rendered with the model's thinking mode
	// named, since the answer carried reasoning and the request did not name
	// the mode.
	thinkingNamed bool
}

func (c *usageCount) logFields(claim uint64) []any {
	ratio := 0.0
	if c.output > 0 {
		ratio = float64(claim) / float64(c.output)
	}
	return []any{
		"positions", c.positions,
		"excluded", c.excluded,
		"rendered_output", c.output,
		"output_limit", c.outputLimit,
		"output_skipped", c.outputSkipped,
		"ratio_out", ratio,
		"rendered_input", c.input,
		"input_limit", c.inputLimit,
		"input_skipped", c.inputSkipped,
		"output_render", c.outputRender,
		"input_render", c.inputRender,
		"output_render_nodes", c.outputRenderNodes,
		"input_render_nodes", c.inputRenderNodes,
		"thinking_named", c.thinkingNamed,
	}
}

// asMap reads a JSON object decoded into any, nil for anything else.
func asMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// fetchedVerdict counts a stored body whose hashes matched: its positions
// against the claimed output tokens, then the served turn and the request
// through the model's renderer. A rule of the request, the same for every
// voter, leaves a rendered check out; the positions are always counted.
// reject is set when this host cannot do a check the rule asks for: then
// it does not vote, as NONE means every such check passed.
func (h *Host) fetchedVerdict(ctx context.Context, fetch devshard.ChallengeFetch, rec types.InferenceRecord) (verdict keptChallengeVote, count *usageCount, reject string, cause string) {
	count = &usageCount{}
	answer, err := parseServedAnswer(fetch.Response)
	readable := err == nil
	if !readable {
		answer = servedAnswer{}
	}
	count.positions = answer.positions
	// The executor's devshardd forces logprobs on every request it runs, so
	// an honest body never has zero positions. The exception is the
	// reasoning model whose answer can come back empty when vLLM strips the
	// only tokens it generated (</think> at a small max_tokens): with
	// completion tokens claimed, an empty body is its documented outcome,
	// which the gateway does not quarantine and replay passes. A body of
	// that model with positions, or one that cannot be read, is counted like
	// any other.
	if answer.positions == 0 {
		if readable && types.ModelBurnsEmpty(rec.Model) && rec.OutputTokens > 0 {
			if terms, err := readExecutedTerms(fetch.Request); err == nil && terms.maxTokens > 0 && rec.OutputTokens > terms.maxTokens {
				return inflated(types.ChallengeCheck_CHALLENGE_CHECK_POSITIONS, rec.OutputTokens, terms.maxTokens), count, "", ""
			}
			count.outputSkipped = "model_burns_empty"
			count.inputSkipped = "model_burns_empty"
			return keptChallengeVote{Fault: types.ChallengeFault_CHALLENGE_FAULT_NONE}, count, "", ""
		}
		return keptChallengeVote{
			Fault:     types.ChallengeFault_CHALLENGE_FAULT_EMPTY_STREAM,
			Check:     types.ChallengeCheck_CHALLENGE_CHECK_POSITIONS,
			Deviation: int64(rec.OutputTokens),
		}, count, "", ""
	}
	if rec.OutputTokens > answer.positions+positionSlack {
		return inflated(types.ChallengeCheck_CHALLENGE_CHECK_POSITIONS, rec.OutputTokens, answer.positions), count, "", ""
	}

	var terms executedTerms
	termsErr := errors.New("no executed request")
	if fetch.Request != nil {
		terms, termsErr = readExecutedTerms(fetch.Request)
	}
	// The engine stops at max_tokens, for any model: a claim past it was not
	// generated.
	if termsErr == nil && terms.maxTokens > 0 && rec.OutputTokens > terms.maxTokens {
		return inflated(types.ChallengeCheck_CHALLENGE_CHECK_POSITIONS, rec.OutputTokens, terms.maxTokens), count, "", ""
	}
	allowance, known := types.UsageAllowanceFor(rec.Model)
	switch {
	case !known:
		count.outputSkipped, count.inputSkipped = "model_not_in_table", "model_not_in_table"
	case fetch.Request == nil:
		known = false
		count.outputSkipped, count.inputSkipped = "no_request", "no_request"
	case termsErr != nil:
		known = false
		count.outputSkipped, count.inputSkipped = "request_unreadable", "request_unreadable"
	}
	request := fetch.Request
	if known && answer.reasoning != "" && allowance.ThinkingKwarg != "" {
		if request, count.thinkingNamed, err = withThinking(request, allowance.ThinkingKwarg); err != nil {
			known = false
			count.outputSkipped, count.inputSkipped = "request_unreadable", "request_unreadable"
		}
	}
	if answer.brokenEarlierCall() {
		return inflated(types.ChallengeCheck_CHALLENGE_CHECK_RENDERED_OUTPUT, rec.OutputTokens, 0), count, "", ""
	}
	countOutput := known
	if countOutput {
		switch {
		case terms.constrained:
			count.outputSkipped, countOutput = "grammar_constrained", false
		case terms.tools && rec.OutputTokens == terms.maxTokens && cutInHeldArgument(answer, terms):
			// max_tokens cut the last call inside an argument the parser holds
			// back until it is whole (object, array, number): what the engine
			// generated for it was never served, and a render of the served
			// part counts less. A cut string argument is served as generated
			// and its incomplete JSON is closed only for rendering.
			count.outputSkipped, countOutput = "cut_tool_call", false
		}
	}
	countInput := known
	if countInput && !terms.textOnly {
		count.inputSkipped, countInput = "non_text_request", false
	}
	if !countOutput && !countInput {
		return keptChallengeVote{Fault: types.ChallengeFault_CHALLENGE_FAULT_NONE}, count, "", ""
	}
	if h.renderer == nil || !h.renderer.Serves(rec.Model) {
		return keptChallengeVote{}, count, ChallengeRejectNoRender, "no render server for the model"
	}

	if countOutput {
		turn, err := answer.renderTurn(terms)
		if err != nil {
			return keptChallengeVote{}, count, ChallengeRejectNoRender, err.Error()
		}
		started := time.Now()
		renderCtx := observability.WithRenderNodeObserver(ctx, func(id, endpoint string) {
			count.outputRenderNodes = append(count.outputRenderNodes, id+"@"+endpoint)
		})
		rendered, source, err := h.renderer.CountTurn(renderCtx, rec.Model, request, turn)
		count.outputRender = source
		observeRender("output", rec.Model, started, err)
		switch {
		case err != nil:
			// A refusal is no count either: a vote without the count would be
			// NONE on a body nobody counted.
			return keptChallengeVote{}, count, renderReject(err), err.Error()
		default:
			count.output = rendered
			// A cut can leave a character unfinished, its bytes counted and
			// never served. Tokens forced by a client logit_bias, which can be
			// byte fragments that decode to no text (cyberdelamain's review of
			// gonka-ai/gonka#2000), no longer reach the engine: the gateway
			// rejects logit_bias and the executor drops it.
			if answer.finish == "length" {
				count.excluded += min(answer.trailingEmpty, utf8TailPositions)
			}
			claim := rec.OutputTokens - min(rec.OutputTokens, count.excluded)
			count.outputLimit = terms.minTokens + 2*rendered + allowance.ServiceTokens + terms.stopBytes +
				uint64(len(answer.calls))*allowance.ToolCallHeader
			if claim > count.outputLimit {
				return inflated(types.ChallengeCheck_CHALLENGE_CHECK_RENDERED_OUTPUT, rec.OutputTokens, rendered), count, "", ""
			}
		}
	}
	if countInput {
		started := time.Now()
		renderCtx := observability.WithRenderNodeObserver(ctx, func(id, endpoint string) {
			count.inputRenderNodes = append(count.inputRenderNodes, id+"@"+endpoint)
		})
		rendered, source, err := h.renderer.CountPrompt(renderCtx, rec.Model, request)
		count.inputRender = source
		observeRender("input", rec.Model, started, err)
		switch {
		case err != nil:
			return keptChallengeVote{}, count, renderReject(err), err.Error()
		default:
			count.input = rendered
			count.inputLimit = rendered + allowance.InputSpread
			if rec.InputTokens > count.inputLimit {
				return inflated(types.ChallengeCheck_CHALLENGE_CHECK_RENDERED_INPUT, rec.InputTokens, rendered), count, "", ""
			}
		}
	}
	return keptChallengeVote{Fault: types.ChallengeFault_CHALLENGE_FAULT_NONE}, count, "", ""
}

// cutInHeldArgument reports a last call cut inside an argument the request's
// schema does not type as a string, or one whose type the voter cannot tell.
func cutInHeldArgument(answer servedAnswer, terms executedTerms) bool {
	call, cut := answer.cutCall()
	if !cut {
		return false
	}
	argument, ok := openArgument(call.arguments)
	return !ok || !terms.stringArgument(call.name, argument)
}

// renderReject is the reject cause of a count that failed: render_busy when
// no ML node was free and no render server counted, so the vote is tried
// again inside the window, no_render otherwise.
func renderReject(err error) string {
	if errors.Is(err, errRenderBusy) {
		return ChallengeRejectRenderBusy
	}
	return ChallengeRejectNoRender
}

func observeRender(count, model string, started time.Time, err error) {
	outcome := "ok"
	switch {
	case errors.Is(err, errRenderRefused):
		outcome = "refused"
	case errors.Is(err, errRenderBusy):
		outcome = "busy"
	case err != nil:
		outcome = "failed"
	}
	if _, known := types.UsageAllowanceFor(model); !known {
		model = "other"
	}
	observability.ObserveChallengeRender(count, model, outcome, time.Since(started).Seconds())
}

func inflated(check types.ChallengeCheck, claim, counted uint64) keptChallengeVote {
	return keptChallengeVote{
		Fault:     types.ChallengeFault_CHALLENGE_FAULT_USAGE_INFLATED,
		Check:     check,
		Deviation: int64(claim - min(claim, counted)),
	}
}
