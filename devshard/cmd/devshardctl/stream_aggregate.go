package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync/atomic"
)

// aggregateMaxSSEEventBytes is the scanner cap for one SSE data line (1 MiB),
// matching the donor transport.DefaultMaxSSEEventBytes. gateway-v4 does not
// export that constant; keep the fold self-contained.
const aggregateMaxSSEEventBytes = 1 << 20

// aggregateDroppedTrailingErrorTotal counts error-shaped SSE payloads ignored
// after a choice already had a terminal finish_reason (defense-in-depth for
// malformed upstream that appends noise after a completed answer).
var aggregateDroppedTrailingErrorTotal atomic.Uint64

// aggregateDroppedChoiceFanoutTotal counts choices[].index values ignored once
// the fold already holds aggregateMaxChoices distinct indexes.
var aggregateDroppedChoiceFanoutTotal atomic.Uint64

// aggregateDroppedToolCallFanoutTotal counts tool_calls[].index values ignored
// once a choice already holds aggregateMaxToolCallsPerChoice.
var aggregateDroppedToolCallFanoutTotal atomic.Uint64

// aggregateDroppedExtrasFanoutTotal counts top-level / choice-extra keys ignored
// once the corresponding extras map is at aggregateMaxExtrasKeys.
var aggregateDroppedExtrasFanoutTotal atomic.Uint64

const (
	noResponseDataJSON = `{"error":{"message":"no response data"}}`

	// Returned when the reader hits a non-EOF error (spool read failure,
	// oversize line) and nothing usable was folded. Distinguishable from
	// noResponseDataJSON so callers/tests can tell a truncated stream from an
	// empty one (R5).
	aggregateStreamReadFailedJSON = `{"error":{"message":"aggregate stream read failed"}}`

	// Returned when fold state (text + logprobs) exceeds the fold byte budget (R4).
	aggregateFoldTooLargeJSON = `{"error":{"message":"aggregate fold exceeds size limit"}}`

	// Fan-out caps for the map-based fold (hostile index/key amplification).
	// Step 13 forces request n=1 on the wire; the choice cap is a hard safety
	// bound (not "honor client n") so a 16 MiB body cannot allocate hundreds of
	// thousands of aggChoice maps. Tool-call and extras caps similarly bound
	// extension-preserving maps.
	aggregateMaxChoices            = 8
	aggregateMaxToolCallsPerChoice = 64
	aggregateMaxExtrasKeys         = 64
)

// sseErrorKeyMarker is quoted so it matches the two payload shapes
// jsonErrorPayloadDetails accepts — {"error":{…}} and {"object":"error",…} —
// without matching the word "error" inside generated content, which is common
// enough in code-assistant traffic to make a bare-word marker useless.
var sseErrorKeyMarker = []byte(`"error"`)

// Text fields on choices[].delta / choices[].message that fragment across
// SSE chunks and must be concatenated rather than first-writer-wins.
var concatMessageTextKeys = map[string]struct{}{
	"content":           {},
	"reasoning":         {},
	"reasoning_content": {},
	"refusal":           {},
}

// aggregateSSEStream folds an SSE (or SSE-shaped) upstream body into one
// chat.completion JSON object for non-streaming clients.
//
// Folding is map-based: known fragmentable text fields are concatenated,
// tool_calls are merged by index, finish_reason / stop_reason / usage take the
// last non-null value, and every other key on the chunk / choice / message is
// first-writer-wins. Provider extensions therefore survive aggregation.
//
// intent controls client-facing logprobs: when the client did not ask for them,
// they are never accumulated or emitted (F7), so the caller need not re-parse
// the aggregate just to strip them. Internal fields (token_ids, etc.) are
// dropped while folding; passthrough bodies are filtered only when
// containsAnyInternalField is true.
//
// Passthrough: a single chat.completion (message-shaped) event, or a host
// error payload, is returned unchanged (after the optional filter). An input
// with no usable data returns the same {"error":{"message":"no response data"}}
// body assembleSSEChunks did.
//
// SSE folding is delegated to aggregateSSEStreamReader so the bytes helper and
// the production reader share one line-scan implementation (including the
// aggregateMaxSSEEventBytes line cap). Non-SSE shapes — {"events":[…]} envelopes
// and bare chat.completion JSON — remain as a bytes-only fallback for
// cache/replay and unit tests; handleNonStreaming never produces them.
func aggregateSSEStream(raw []byte, intent clientResponseIntent) []byte {
	out := aggregateSSEStreamReader(bytes.NewReader(raw), intent)
	if !isAggregateNoResponseData(out) {
		return out
	}
	if events, ok := payloadsFromEventsEnvelope(raw); ok {
		return aggregateFromPayloads(events, intent)
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '{' {
		return aggregateFromPayloads([][]byte{trimmed}, intent)
	}
	return out
}

func isAggregateNoResponseData(b []byte) bool {
	return bytes.Equal(bytes.TrimSpace(b), []byte(noResponseDataJSON))
}

func isAggregateFoldTooLargePayload(b []byte) bool {
	details, ok := jsonErrorPayloadDetails(b)
	return ok && details.Message == ErrAggregateFoldTooLarge.Error()
}

// aggregateSSEStreamReader folds an SSE stream from r without requiring the
// full body in a contiguous []byte. Used by handleAggregated after OpenReader
// so spilled spool files are scanned line-by-line (peak RAM ≈ one SSE line +
// fold state + marshaled output, not 2× the wire body).
//
// On a non-EOF read error (spool fault, oversize line), a usable fold is
// returned rather than discarded; if nothing was folded, the response is
// aggregateStreamReadFailedJSON (R5).
func aggregateSSEStreamReader(r io.Reader, intent clientResponseIntent) []byte {
	if r == nil {
		return []byte(noResponseDataJSON)
	}
	sc := bufio.NewScanner(r)
	// Step 12 caps a single SSE event at 1 MiB; allow that plus framing headroom.
	maxLine := aggregateMaxSSEEventBytes + 64
	sc.Buffer(make([]byte, 64<<10), maxLine)

	folder := newCompletionFolder(intent)
	defer folder.close()
	var (
		first []byte
		count int
		event []byte
	)
	ingestEvent := func() []byte {
		data := bytes.TrimSpace(event)
		event = event[:0]
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			return nil
		}
		count++
		if count == 1 {
			first = append([]byte(nil), data...)
		}
		if out, early := folder.ingest(data); early {
			return out
		}
		return nil
	}
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			if out := ingestEvent(); out != nil {
				return out
			}
			continue
		}
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
		if len(event) > 0 {
			event = append(event, '\n')
		}
		event = append(event, payload...)
	}
	if out := ingestEvent(); out != nil {
		return out
	}
	readErr := sc.Err()
	if count == 0 {
		if readErr != nil {
			log.Printf("aggregate_sse: read failed with empty fold (max_line_bytes=%d): %v", maxLine, readErr)
			return []byte(aggregateStreamReadFailedJSON)
		}
		return []byte(noResponseDataJSON)
	}
	// Host errors already early-returned from ingest; only bare chat.completion
	// messages need a post-scan passthrough (ingest folds them as chunks).
	if count == 1 && isChatCompletionMessagePayload(first) {
		if readErr != nil {
			log.Printf("aggregate_sse: read failed after single payload (max_line_bytes=%d): %v", maxLine, readErr)
		}
		return filterPassthroughPayload(first, intent)
	}
	out, ok := folder.result()
	if ok {
		if readErr != nil {
			log.Printf("aggregate_sse: read failed; keeping usable fold payloads=%d (max_line_bytes=%d): %v", count, maxLine, readErr)
		}
		return out
	}
	if readErr != nil {
		log.Printf("aggregate_sse: read failed with unusable fold payloads=%d (max_line_bytes=%d): %v", count, maxLine, readErr)
		return []byte(aggregateStreamReadFailedJSON)
	}
	return []byte(noResponseDataJSON)
}

func aggregateFromPayloads(payloads [][]byte, intent clientResponseIntent) []byte {
	if len(payloads) == 0 {
		return []byte(noResponseDataJSON)
	}

	if len(payloads) == 1 {
		p := payloads[0]
		if isHostErrorPayload(p) || isChatCompletionMessagePayload(p) {
			return filterPassthroughPayload(p, intent)
		}
	}

	out, ok := foldCompletionChunks(payloads, intent)
	if !ok {
		if isAggregateFoldTooLargePayload(out) {
			return out
		}
		return []byte(noResponseDataJSON)
	}
	return out
}

func filterPassthroughPayload(p []byte, intent clientResponseIntent) []byte {
	if !containsAnyInternalField(p) {
		return append([]byte(nil), p...)
	}
	return filterClientInternalFields(p, intent)
}

func payloadsFromEventsEnvelope(raw []byte) ([][]byte, bool) {
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(raw), &generic); err != nil {
		return nil, false
	}
	eventsRaw, exists := generic["events"]
	if !exists {
		return nil, false
	}
	var events []string
	if err := json.Unmarshal(eventsRaw, &events); err != nil {
		return nil, false
	}
	out := make([][]byte, 0, len(events))
	for _, event := range events {
		line := strings.TrimRight(event, "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		data := line
		if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if data == "" || data == "[DONE]" {
			continue
		}
		out = append(out, []byte(data))
	}
	if len(out) == 0 {
		return nil, true
	}
	return out, true
}

func isHostErrorPayload(p []byte) bool {
	// F8: most chunks are completions; skip the full error-shaped unmarshal
	// unless the bytes could plausibly be an error object.
	if len(p) == 0 || !bytes.Contains(p, sseErrorKeyMarker) {
		return false
	}
	_, ok := jsonErrorPayloadDetails(p)
	return ok
}

func isChatCompletionMessagePayload(p []byte) bool {
	var probe struct {
		Object  string `json:"object"`
		Choices []struct {
			Message *json.RawMessage `json:"message"`
			Delta   *json.RawMessage `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(p, &probe); err != nil || len(probe.Choices) == 0 {
		return false
	}
	hasMessage := false
	for _, c := range probe.Choices {
		if c.Message != nil {
			hasMessage = true
		}
		if c.Delta != nil {
			return false
		}
	}
	if !hasMessage {
		return false
	}
	if probe.Object == "" || probe.Object == "chat.completion" {
		return true
	}
	return false
}

type aggToolCall struct {
	fields map[string]any
	fn     map[string]any
	args   strings.Builder
}

type aggChoice struct {
	index        int
	msg          map[string]any
	text         map[string]*strings.Builder
	toolCalls    map[int]*aggToolCall
	toolOrder    []int
	lp           logprobStore
	finishReason any
	hasFinish    bool
	stopReason   any
	hasStop      bool
	extras       map[string]any
}

// foldBudget bounds the bytes one fold may hold. It is request-wide rather
// than per choice: every text builder, tool-call argument buffer, extras value
// and logprobs entry across all choices draws on the same two ceilings, so
// fan-out cannot multiply the budget (R4).
type foldBudget struct {
	ramBytes  int64 // text + tool args + extras + in-mem logprobs
	diskBytes int64 // logprobs spilled to the spool dir
	ramLimit  int64
	diskLimit int64
	spoolDir  string
}

type completionFolder struct {
	foldBudget
	intent     clientResponseIntent
	strip      []string
	choices    map[int]*aggChoice
	top        map[string]any
	usage      any
	haveUsage  bool
	sawUsable  bool
	firstError []byte
	// once-per-fold warn flags so hostile fan-out does not spam logs
	warnedChoiceFanout   bool
	warnedToolCallFanout bool
	warnedExtrasFanout   bool
	warnedFoldTooLarge   bool
}

func newCompletionFolder(intent clientResponseIntent) *completionFolder {
	ram, disk, spool := currentAggregateBufferConfig()
	if ram <= 0 {
		ram = defaultAggregateMaxMemoryBytes
	}
	if disk <= 0 {
		disk = defaultAggregateMaxResponseBytes
	}
	return &completionFolder{
		foldBudget: foldBudget{ramLimit: ram, diskLimit: disk, spoolDir: spool},
		intent:     intent,
		strip:      intent.strippedFields(),
		choices:    map[int]*aggChoice{},
		top:        map[string]any{},
	}
}

func (f *completionFolder) close() {
	if f == nil {
		return
	}
	for _, ac := range f.choices {
		if ac != nil {
			ac.lp.close()
		}
	}
}

func foldCompletionChunks(payloads [][]byte, intent clientResponseIntent) ([]byte, bool) {
	f := newCompletionFolder(intent)
	defer f.close()
	for _, p := range payloads {
		if out, early := f.ingest(p); early {
			return out, true
		}
	}
	return f.result()
}

func (f *completionFolder) noteDroppedChoiceFanout() {
	aggregateDroppedChoiceFanoutTotal.Add(1)
	if f == nil || f.warnedChoiceFanout {
		return
	}
	f.warnedChoiceFanout = true
	log.Printf("aggregate_fold: choice fan-out cap hit (max_choices=%d)", aggregateMaxChoices)
}

func (f *completionFolder) noteDroppedToolCallFanout() {
	aggregateDroppedToolCallFanoutTotal.Add(1)
	if f == nil || f.warnedToolCallFanout {
		return
	}
	f.warnedToolCallFanout = true
	log.Printf("aggregate_fold: tool_call fan-out cap hit (max_tool_calls_per_choice=%d)", aggregateMaxToolCallsPerChoice)
}

func (f *completionFolder) noteDroppedExtrasFanout() {
	aggregateDroppedExtrasFanoutTotal.Add(1)
	if f == nil || f.warnedExtrasFanout {
		return
	}
	f.warnedExtrasFanout = true
	log.Printf("aggregate_fold: extras fan-out cap hit (max_extras_keys=%d)", aggregateMaxExtrasKeys)
}

func (f *completionFolder) foldTooLargeResult() []byte {
	if f != nil && !f.warnedFoldTooLarge {
		f.warnedFoldTooLarge = true
		log.Printf("aggregate_fold: size budget exceeded ram_limit=%d disk_limit=%d", f.ramLimit, f.diskLimit)
	}
	return []byte(aggregateFoldTooLargeJSON)
}

// ingest folds one SSE data payload. early=true means the caller should return
// out immediately (sole host error before any usable content, or fold oversize).
func (f *completionFolder) ingest(p []byte) (out []byte, early bool) {
	if isHostErrorPayload(p) {
		if !f.sawUsable {
			return append([]byte(nil), p...), true
		}
		if hasTerminalFinishReason(f.choices) {
			aggregateDroppedTrailingErrorTotal.Add(1)
			log.Printf("aggregate_fold: dropped trailing error payload after terminal finish_reason")
			return nil, false
		}
		if f.firstError == nil {
			f.firstError = append([]byte(nil), p...)
		}
		return nil, false
	}
	if normalized, replaced := replaceNonFiniteNumbers(p); replaced {
		p = normalized
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(p, &raw); err != nil {
		return nil, false
	}

	if err := f.mergeTopLevelRawFirstWriter(raw); err != nil {
		return f.foldTooLargeResult(), true
	}

	if u, ok := raw["usage"]; ok {
		if usageVal, ok := decodeNonNullJSON(u); ok {
			f.usage = usageVal
			f.haveUsage = true
		}
	}

	choiceRaws, ok := decodeChoiceRawSlice(raw["choices"])
	for _, chRaw := range choiceRaws {
		ch, lpRaw, err := decodeChoiceForFold(chRaw, f.intent)
		if err != nil || ch == nil {
			continue
		}
		idx := jsonIndex(ch["index"])
		// Step 13 forces n=1; reject hostile / out-of-range indexes instead of
		// allocating an aggChoice per distinct index in a 16 MiB body.
		if idx < 0 || idx >= aggregateMaxChoices {
			f.noteDroppedChoiceFanout()
			continue
		}
		ac := f.choices[idx]
		if ac == nil {
			ac = &aggChoice{
				index:     idx,
				msg:       map[string]any{},
				text:      map[string]*strings.Builder{},
				toolCalls: map[int]*aggToolCall{},
				extras:    map[string]any{},
			}
			f.choices[idx] = ac
		}
		f.sawUsable = true

		if delta, ok := ch["delta"].(map[string]any); ok {
			if err := f.mergeMessageFragment(ac, delta); err != nil {
				return f.foldTooLargeResult(), true
			}
		}
		if msg, ok := ch["message"].(map[string]any); ok {
			if err := f.mergeMessageFragment(ac, msg); err != nil {
				return f.foldTooLargeResult(), true
			}
		}
		if f.intent.keepLogprobs && len(bytes.TrimSpace(lpRaw)) > 0 {
			if err := f.accumulateChoiceLogprobs(ac, lpRaw); err != nil {
				return f.foldTooLargeResult(), true
			}
		}
		if fr, ok := ch["finish_reason"]; ok && fr != nil {
			ac.finishReason = fr
			ac.hasFinish = true
		}
		if sr, ok := ch["stop_reason"]; ok && sr != nil {
			ac.stopReason = sr
			ac.hasStop = true
		}
		for k, v := range ch {
			switch k {
			case "index", "delta", "message", "logprobs", "finish_reason", "stop_reason":
				continue
			}
			if isStrippedKey(k, f.strip) || v == nil {
				continue
			}
			if _, exists := ac.extras[k]; exists {
				continue
			}
			if len(ac.extras) >= aggregateMaxExtrasKeys {
				f.noteDroppedExtrasFanout()
				continue
			}
			if err := f.chargeRAM(estimateFoldValueBytes(v)); err != nil {
				return f.foldTooLargeResult(), true
			}
			ac.extras[k] = v
		}
	}
	if ok && len(choiceRaws) == 0 {
		if u, has := raw["usage"]; has {
			if _, ok := decodeNonNullJSON(u); ok {
				f.sawUsable = true
			}
		}
	}
	return nil, false
}

func (b *foldBudget) ramAvailable(delta int64) bool {
	return b.ramBytes+delta <= b.ramLimit
}

func (b *foldBudget) chargeRAM(delta int64) error {
	if delta <= 0 {
		return nil
	}
	if !b.ramAvailable(delta) {
		return fmt.Errorf("%w: fold ram %d+%d > %d", ErrAggregateFoldTooLarge, b.ramBytes, delta, b.ramLimit)
	}
	b.ramBytes += delta
	return nil
}

func (b *foldBudget) chargeDisk(delta int64) error {
	if delta <= 0 {
		return nil
	}
	if b.diskBytes+delta > b.diskLimit {
		return fmt.Errorf("%w: fold disk %d+%d > %d", ErrAggregateFoldTooLarge, b.diskBytes, delta, b.diskLimit)
	}
	b.diskBytes += delta
	return nil
}

// moveToDisk reclassifies bytes already charged to RAM as disk bytes when a
// store spills, so the two counters stay exact across the switch.
func (b *foldBudget) moveToDisk(n int64) error {
	if err := b.chargeDisk(n); err != nil {
		return err
	}
	b.ramBytes -= n
	return nil
}

func estimateFoldValueBytes(v any) int64 {
	switch t := v.(type) {
	case string:
		return int64(len(t))
	case []byte:
		return int64(len(t))
	case json.RawMessage:
		return int64(len(t))
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return 64
		}
		return int64(len(b))
	}
}

func (f *completionFolder) result() ([]byte, bool) {
	if f.firstError != nil && !hasTerminalFinishReason(f.choices) {
		return f.firstError, true
	}
	if len(f.choices) == 0 {
		return nil, false
	}

	indexes := make([]int, 0, len(f.choices))
	for idx := range f.choices {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)

	outChoices := make([]any, 0, len(indexes))
	for _, idx := range indexes {
		choice, err := buildChoice(f.choices[idx])
		if err != nil {
			// A spool fault while reading logprobs back must not look like
			// "the host sent no logprobs" (R4); surface it like any other
			// read failure instead of silently emitting logprobs:null.
			log.Printf("aggregate_fold: logprobs emit failed for choice %d: %v", idx, err)
			return []byte(aggregateStreamReadFailedJSON), true
		}
		outChoices = append(outChoices, choice)
	}

	result := map[string]any{
		"object":  "chat.completion",
		"choices": outChoices,
	}
	for k, v := range f.top {
		if k == "object" || k == "choices" || k == "usage" {
			continue
		}
		result[k] = v
	}
	if _, ok := result["id"]; !ok {
		result["id"] = ""
	}
	if _, ok := result["created"]; !ok {
		result["created"] = float64(0)
	}
	if _, ok := result["model"]; !ok {
		result["model"] = ""
	}
	if f.haveUsage {
		result["usage"] = f.usage
	}

	b, err := json.Marshal(result)
	if err != nil {
		return nil, false
	}
	return b, true
}

func hasTerminalFinishReason(choices map[int]*aggChoice) bool {
	for _, ac := range choices {
		if ac != nil && ac.hasFinish {
			return true
		}
	}
	return false
}

func (f *completionFolder) accumulateChoiceLogprobs(ac *aggChoice, lpRaw json.RawMessage) error {
	content, extras, err := parseLogprobsContentRaw(lpRaw)
	if err != nil {
		return nil // ignore malformed logprobs object
	}
	// The store charges the shared budget itself: RAM while it fits, spool
	// bytes once it does not.
	if err := ac.lp.appendEntries(content, !f.intent.keepTopLogprobs, &f.foldBudget); err != nil {
		return err
	}

	if len(extras) == 0 {
		return nil
	}
	lpOut, _ := ac.extras["logprobs"].(map[string]any)
	if lpOut == nil {
		if len(ac.extras) >= aggregateMaxExtrasKeys {
			f.noteDroppedExtrasFanout()
			return nil
		}
		lpOut = map[string]any{}
		ac.extras["logprobs"] = lpOut
	}
	for k, v := range extras {
		if k == "logprob" || k == "token_ids" || k == "prompt_logprobs" || k == "prompt_token_ids" {
			continue
		}
		trimmed := bytes.TrimSpace(v)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			continue
		}
		if _, exists := lpOut[k]; exists {
			continue
		}
		if len(lpOut) >= aggregateMaxExtrasKeys {
			f.noteDroppedExtrasFanout()
			continue
		}
		val, ok := decodeNonNullJSON(trimmed)
		if !ok {
			continue
		}
		if err := f.chargeRAM(estimateFoldValueBytes(val)); err != nil {
			return err
		}
		lpOut[k] = val
	}
	return nil
}

func isStrippedKey(k string, strip []string) bool {
	for _, s := range strip {
		if k == s {
			return true
		}
	}
	return false
}

// foldReservedTopKeys always fit under the extras cap so a hostile flood of
// unknown top-level keys cannot displace id/created/model.
var foldReservedTopKeys = map[string]struct{}{
	"id":                 {},
	"created":            {},
	"model":              {},
	"system_fingerprint": {},
	"service_tier":       {},
}

func (f *completionFolder) mergeTopLevelRawFirstWriter(src map[string]json.RawMessage) error {
	dst := f.top
	for k, v := range src {
		switch k {
		case "choices", "usage", "object":
			continue
		}
		if isStrippedKey(k, f.strip) {
			continue
		}
		if _, exists := dst[k]; exists {
			continue
		}
		_, reserved := foldReservedTopKeys[k]
		if !reserved && nonReservedTopKeyCount(dst) >= aggregateMaxExtrasKeys {
			f.noteDroppedExtrasFanout()
			continue
		}
		val, ok := decodeNonNullJSON(v)
		if !ok {
			continue
		}
		if !reserved {
			if err := f.chargeRAM(estimateFoldValueBytes(val)); err != nil {
				return err
			}
		}
		dst[k] = val
	}
	return nil
}

func nonReservedTopKeyCount(dst map[string]any) int {
	n := 0
	for k := range dst {
		if _, reserved := foldReservedTopKeys[k]; !reserved {
			n++
		}
	}
	return n
}

func decodeNonNullJSON(raw json.RawMessage) (any, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, false
	}
	var val any
	if err := json.Unmarshal(trimmed, &val); err != nil || val == nil {
		return nil, false
	}
	return val, true
}

func decodeChoiceRawSlice(raw json.RawMessage) ([]json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, true
	}
	var out []json.RawMessage
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil, false
	}
	return out, true
}

// decodeChoiceForFold expands one choices[] element. When !keepLogprobs, the
// logprobs member is discarded (F11). When keepLogprobs, logprobs are returned
// as raw JSON so content entries can be stored as NDJSON without map trees (R4).
func decodeChoiceForFold(chRaw json.RawMessage, intent clientResponseIntent) (map[string]any, json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(chRaw, &raw); err != nil {
		return nil, nil, err
	}
	var lpRaw json.RawMessage
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		if k == "logprobs" {
			if intent.keepLogprobs {
				// Aliases the caller's payload; consumed within this ingest
				// (the store copies the bytes it keeps), so no copy is needed.
				lpRaw = v
			}
			continue
		}
		var val any
		if err := json.Unmarshal(v, &val); err != nil {
			continue
		}
		out[k] = val
	}
	return out, lpRaw, nil
}

func (f *completionFolder) mergeMessageFragment(ac *aggChoice, frag map[string]any) error {
	for k, v := range frag {
		if isStrippedKey(k, f.strip) {
			continue
		}
		if k == "tool_calls" {
			if tcs, ok := v.([]any); ok {
				for _, raw := range tcs {
					if tc, ok := raw.(map[string]any); ok {
						if err := f.mergeToolCallMap(ac, tc); err != nil {
							return err
						}
					}
				}
			}
			continue
		}
		if _, concat := concatMessageTextKeys[k]; concat {
			if err := f.appendMessageText(ac, k, v); err != nil {
				return err
			}
			continue
		}
		if v == nil {
			continue
		}
		if _, exists := ac.msg[k]; !exists {
			if err := f.chargeRAM(estimateFoldValueBytes(v)); err != nil {
				return err
			}
			ac.msg[k] = v
		}
	}
	return nil
}

func (f *completionFolder) appendMessageText(ac *aggChoice, key string, v any) error {
	if v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		// Non-string payload for a normally-textual key: first-writer-wins.
		if _, exists := ac.msg[key]; !exists {
			if b := ac.text[key]; b == nil || b.Len() == 0 {
				if err := f.chargeRAM(estimateFoldValueBytes(v)); err != nil {
					return err
				}
				ac.msg[key] = v
			}
		}
		return nil
	}
	if s == "" {
		return nil
	}
	if err := f.chargeRAM(int64(len(s))); err != nil {
		return err
	}
	b := ac.text[key]
	if b == nil {
		b = &strings.Builder{}
		ac.text[key] = b
	}
	b.WriteString(s)
	return nil
}

func (f *completionFolder) mergeToolCallMap(ac *aggChoice, tc map[string]any) error {
	idx := jsonIndex(tc["index"])
	existing := ac.toolCalls[idx]
	if existing == nil {
		if len(ac.toolCalls) >= aggregateMaxToolCallsPerChoice {
			f.noteDroppedToolCallFanout()
			return nil
		}
		existing = &aggToolCall{
			fields: map[string]any{},
			fn:     map[string]any{},
		}
		ac.toolCalls[idx] = existing
		ac.toolOrder = append(ac.toolOrder, idx)
	}
	for k, v := range tc {
		switch k {
		case "index":
			existing.fields["index"] = idx
		case "function":
			fn, _ := v.(map[string]any)
			if fn == nil {
				continue
			}
			for fk, fv := range fn {
				if fk == "arguments" {
					if s, ok := fv.(string); ok {
						prev := existing.args.String()
						// Some backends restate the whole arguments string each
						// chunk. Appending those would produce unparseable JSON.
						if prev != "" && strings.HasPrefix(s, prev) {
							if err := f.chargeRAM(int64(len(s) - len(prev))); err != nil {
								return err
							}
							existing.args.Reset()
							existing.args.WriteString(s)
						} else {
							if err := f.chargeRAM(int64(len(s))); err != nil {
								return err
							}
							existing.args.WriteString(s)
						}
					} else if fv != nil {
						if _, exists := existing.fn[fk]; !exists {
							if err := f.chargeRAM(estimateFoldValueBytes(fv)); err != nil {
								return err
							}
							existing.fn[fk] = fv
						}
					}
					continue
				}
				if fv == nil {
					continue
				}
				if s, ok := fv.(string); ok && s == "" {
					continue
				}
				if _, exists := existing.fn[fk]; !exists {
					if err := f.chargeRAM(estimateFoldValueBytes(fv)); err != nil {
						return err
					}
					existing.fn[fk] = fv
				}
			}
		default:
			if v == nil {
				continue
			}
			if s, ok := v.(string); ok && s == "" {
				continue
			}
			if _, exists := existing.fields[k]; !exists {
				if err := f.chargeRAM(estimateFoldValueBytes(v)); err != nil {
					return err
				}
				existing.fields[k] = v
			}
		}
	}
	return nil
}

func buildChoice(ac *aggChoice) (map[string]any, error) {
	msg := map[string]any{}
	for k, v := range ac.msg {
		msg[k] = v
	}
	for k, b := range ac.text {
		if b != nil && b.Len() > 0 {
			msg[k] = b.String()
		}
	}
	if _, ok := msg["role"]; !ok || msg["role"] == "" {
		msg["role"] = "assistant"
	}

	toolCalls := buildToolCallsMap(ac)
	content, hasContent := msg["content"]
	contentStr, contentIsStr := content.(string)
	if len(toolCalls) > 0 && (!hasContent || (contentIsStr && contentStr == "")) {
		msg["content"] = nil
		msg["tool_calls"] = toolCalls
	} else {
		if !hasContent {
			msg["content"] = ""
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
	}

	choice := map[string]any{
		"index":   ac.index,
		"message": msg,
	}
	entries, err := ac.lp.contentRawMessages()
	if err != nil {
		return nil, err
	}
	// Siblings of content (refusal[], provider extensions) must survive even
	// when content itself is absent — OpenAI's refusal shape is
	// {"content":null,"refusal":[…]}.
	lp, _ := ac.extras["logprobs"].(map[string]any)
	switch {
	case len(entries) > 0:
		merged := map[string]any{"content": entries}
		for k, v := range lp {
			if _, exists := merged[k]; !exists {
				merged[k] = v
			}
		}
		choice["logprobs"] = merged
	case len(lp) > 0:
		if _, exists := lp["content"]; !exists {
			lp["content"] = nil
		}
		choice["logprobs"] = lp
	default:
		// OpenAI emits "logprobs": null when none arrived (F10).
		choice["logprobs"] = nil
	}
	if ac.hasFinish {
		choice["finish_reason"] = ac.finishReason
	} else {
		choice["finish_reason"] = nil
	}
	if ac.hasStop {
		choice["stop_reason"] = ac.stopReason
	}
	for k, v := range ac.extras {
		if k == "logprobs" {
			continue
		}
		if _, exists := choice[k]; !exists {
			choice[k] = v
		}
	}
	return choice, nil
}

func buildToolCallsMap(ac *aggChoice) []any {
	if len(ac.toolOrder) == 0 {
		return nil
	}
	out := make([]any, 0, len(ac.toolOrder))
	for _, idx := range ac.toolOrder {
		tc := ac.toolCalls[idx]
		item := map[string]any{}
		for k, v := range tc.fields {
			item[k] = v
		}
		if _, ok := item["type"]; !ok || item["type"] == "" {
			item["type"] = "function"
		}
		fn := map[string]any{}
		for k, v := range tc.fn {
			fn[k] = v
		}
		if _, ok := fn["name"]; !ok {
			fn["name"] = ""
		}
		fn["arguments"] = tc.args.String()
		item["function"] = fn
		// OpenAI tool_calls entries are not required to echo index in the
		// final message, and the previous aggregator omitted it.
		delete(item, "index")
		out = append(out, item)
	}
	return out
}

func jsonIndex(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0
		}
		return int(i)
	default:
		return 0
	}
}
