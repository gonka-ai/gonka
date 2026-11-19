package transport

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"strings"

	"devshard/host"
	"devshard/signing"
	"devshard/types"
)

const (
	// DefaultChatFrameJoin is how many content tokens share one gzip chunk
	// after the first chunk, when the gateway asks for nothing else.
	// Must match types.DefaultChatFrameJoin.
	DefaultChatFrameJoin = 5
	// DefaultChatFrameSignEvery is how many chunks share one signature.
	// Must match types.DefaultChatFrameSignEvery.
	DefaultChatFrameSignEvery = 13
	// MaxChatFrameJoin bounds a gateway's request. A joined group holds tokens
	// until it fills. The gateway's inter-chunk stall allowance grows with
	// Join, so a group of this size is the same number of token intervals as
	// the default group is inside the base allowance.
	MaxChatFrameJoin = 64
	// MaxChatFrameSignEvery bounds how many chunks can pass between signatures.
	// The interval does not hold tokens.
	MaxChatFrameSignEvery = 4096
)

func init() {
	if DefaultChatFrameJoin != int(types.DefaultChatFrameJoin) ||
		DefaultChatFrameSignEvery != int(types.DefaultChatFrameSignEvery) {
		panic("transport chat-frame defaults drifted from types")
	}
}

func chatFrameJoin(s *host.ChatFrameSchedule) int {
	if s == nil || s.Join == 0 {
		return DefaultChatFrameJoin
	}
	return int(min(s.Join, MaxChatFrameJoin))
}

func chatFrameSignEvery(s *host.ChatFrameSchedule) int {
	if s == nil || s.SignEvery == 0 {
		return DefaultChatFrameSignEvery
	}
	return int(min(s.SignEvery, MaxChatFrameSignEvery))
}

// newChatStreamHash starts the running hash over a Chat stream's uncompressed
// bytes. The domain, escrow, inference and prompt hash come first, so a
// signature cannot be moved to another inference or another prompt.
func newChatStreamHash(escrowID string, inferenceID uint64, promptHash []byte) hash.Hash {
	h := sha256.New()
	writeLengthPrefixed(h, []byte(types.DomainChatFrame))
	writeLengthPrefixed(h, []byte(escrowID))
	h.Write(binary.BigEndian.AppendUint64(nil, inferenceID))
	writeLengthPrefixed(h, promptHash)
	return h
}

func writeLengthPrefixed(h hash.Hash, b []byte) {
	h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(b))))
	h.Write(b)
}

func chatFramePreimage(escrowID string, inferenceID uint64, contentHash []byte, tokenCount uint64) ([]byte, error) {
	return types.CanonicalSignedBytes(&types.ChatFrameSignatureContent{
		EscrowId:    escrowID,
		InferenceId: inferenceID,
		ContentHash: contentHash,
		TokenCount:  tokenCount,
	})
}

// ChatFrameEvidence is the cleartext a check needs for one signed Chat frame:
// the two ids, the running hash of the prefix, the content-token count in
// that prefix, and the compact signature. The gzip chunk is not one of the
// values, so the check does not inflate it.
type ChatFrameEvidence struct {
	EscrowID    string
	InferenceID uint64
	ContentHash []byte
	TokenCount  uint64
	Sig         []byte
}

// NewChatFrameEvidence copies the hash and the signature. The chunk stays
// with the caller.
func NewChatFrameEvidence(escrowID string, inferenceID uint64, contentHash []byte, tokenCount uint64, sig []byte) ChatFrameEvidence {
	return ChatFrameEvidence{
		EscrowID:    escrowID,
		InferenceID: inferenceID,
		ContentHash: append([]byte(nil), contentHash...),
		TokenCount:  tokenCount,
		Sig:         append([]byte(nil), sig...),
	}
}

// Preimage returns the bytes the executor signs for these values.
func (e ChatFrameEvidence) Preimage() ([]byte, error) {
	return chatFramePreimage(e.EscrowID, e.InferenceID, e.ContentHash, e.TokenCount)
}

// Verify reports whether pubkey signed Preimage.
func (e ChatFrameEvidence) Verify(pubkey []byte) bool {
	preimage, err := e.Preimage()
	if err != nil {
		return false
	}
	return signing.VerifyCompact(pubkey, preimage, e.Sig)
}

// ChatFrameSigner signs the uncompressed prefix of one Chat stream. Every
// signature covers the whole prefix, so a cut stream needs only the newest one.
type ChatFrameSigner struct {
	signer         signing.Signer
	escrowID       string
	inferenceID    uint64
	stream         hash.Hash
	streamBytes    uint64
	signedBytes    uint64
	signedPreimage []byte
}

// NewChatFrameSigner starts the prefix hash for one inference.
func NewChatFrameSigner(signer signing.Signer, escrowID string, inferenceID uint64, promptHash []byte) *ChatFrameSigner {
	return &ChatFrameSigner{
		signer:      signer,
		escrowID:    escrowID,
		inferenceID: inferenceID,
		stream:      newChatStreamHash(escrowID, inferenceID, promptHash),
	}
}

func (s *ChatFrameSigner) add(p []byte) {
	s.stream.Write(p)
	s.streamBytes += uint64(len(p))
}

// hasUnsigned reports bytes the last signature does not cover.
func (s *ChatFrameSigner) hasUnsigned() bool {
	return s.streamBytes > s.signedBytes
}

// sign returns the compact signature over the prefix already absorbed, and
// the number of uncompressed bytes it covers. tokenCount is the content
// tokens in that prefix. The token held back to open the next chunk is not
// part of it, because its bytes are not added until that chunk is sent.
func (s *ChatFrameSigner) sign(tokenCount uint64) ([]byte, uint64, error) {
	preimage, err := chatFramePreimage(s.escrowID, s.inferenceID, s.stream.Sum(nil), tokenCount)
	if err != nil {
		return nil, 0, err
	}
	full, err := s.signer.Sign(preimage)
	if err != nil {
		return nil, 0, fmt.Errorf("sign chat frame: %w", err)
	}
	sig, err := signing.CompactSignature(full)
	if err != nil {
		return nil, 0, err
	}
	s.signedBytes = s.streamBytes
	s.signedPreimage = preimage
	return sig, s.streamBytes, nil
}

// ChatFrameVerifier checks the frame signatures the gateway receives on one
// Chat stream. State holds the executor's addresses, not its key, and a
// compact signature recovers to one key per recovery id. Only the signer's key
// verifies the next signature too, so the candidates narrow by themselves, and
// Close asks whether the key left may sign. Nothing that can reach the chain
// runs while tokens are flowing, and no other key is ever asked about.
//
// The reader feeds every decompressed byte to Write in order. Each signature
// names how many uncompressed bytes it covers, and is checked as soon as the
// hash has reached exactly that many. Failures do not stop the stream; Check
// reports the first.
type ChatFrameVerifier struct {
	escrowID        string
	inferenceID     uint64
	authorized      func(address string) bool
	stream          hash.Hash
	streamBytes     uint64
	candidates      [][]byte
	verified        uint64
	pending         []pendingFrameSig
	lastAt          uint64
	check           host.ChatFrameCheck
	closed          bool
	contentTokens   uint64
	sawFinishReason bool
	sawError        bool
	// notedBytes is how much of the stream NoteEvent has classified. A
	// signature's token count waits until the events in its prefix are noted.
	notedBytes    uint64
	pendingCounts []pendingTokenCount
}

type pendingTokenCount struct {
	at, claimed uint64
}

type pendingFrameSig struct {
	frame, at, tokens uint64
	sig               []byte
}

// maxPendingFrameSigs bounds signatures the reader has not reached yet. A
// frame's bytes inflate as the next frames are read, so only a few wait.
const maxPendingFrameSigs = 64

// NewChatFrameVerifier starts the prefix hash the executor keeps. authorized
// reports whether a recovered address may sign for the executor's slot; it is
// called at most once, from Close.
func NewChatFrameVerifier(escrowID string, inferenceID uint64, promptHash []byte, authorized func(string) bool) *ChatFrameVerifier {
	return &ChatFrameVerifier{
		escrowID:    escrowID,
		inferenceID: inferenceID,
		authorized:  authorized,
		stream:      newChatStreamHash(escrowID, inferenceID, promptHash),
	}
}

// Frame records one received frame. tokenCount is the content-token count
// the signature claims for the prefix streamBytes names. An empty signature
// carries neither.
func (v *ChatFrameVerifier) Frame(sig []byte, streamBytes, tokenCount uint64) {
	v.check.Frames++
	if len(sig) == 0 {
		return
	}
	if v.check.Invalid != "" {
		return
	}
	// Checked before queueing, so a signature waiting for its bytes holds at
	// most CompactSignatureSize of them.
	if len(sig) != signing.CompactSignatureSize {
		v.fail("frame %d: its signature is %d bytes, not %d", v.check.Frames, len(sig), signing.CompactSignatureSize)
		return
	}
	// Each signature covers new bytes. This also bounds the verification work
	// on a stream by its size.
	if streamBytes <= v.lastAt || streamBytes < v.streamBytes {
		v.fail("frame %d: its signature covers %d bytes, behind the stream", v.check.Frames, streamBytes)
		return
	}
	v.lastAt = streamBytes
	if len(v.pending) >= maxPendingFrameSigs {
		v.fail("frame %d: more than %d signatures ahead of the stream", v.check.Frames, maxPendingFrameSigs)
		return
	}
	v.pending = append(v.pending, pendingFrameSig{frame: v.check.Frames, at: streamBytes, tokens: tokenCount, sig: sig})
	v.checkReached()
}

// Write folds decompressed stream bytes into the prefix hash, stopping at
// each byte a pending signature names. It does not parse the events. The
// reader that already has a finished event reports it with NoteEvent, and the
// token count is compared once those events cover the signature's prefix.
func (v *ChatFrameVerifier) Write(p []byte) {
	for len(p) > 0 {
		if len(v.pending) == 0 || v.check.Invalid != "" {
			v.absorb(p)
			return
		}
		take := min(uint64(len(p)), v.pending[0].at-v.streamBytes)
		v.absorb(p[:take])
		p = p[take:]
		v.checkReached()
	}
}

func (v *ChatFrameVerifier) absorb(p []byte) {
	v.stream.Write(p)
	v.streamBytes += uint64(len(p))
}

// maxChatFrameEventScan is the unfinished-event size where the executor stops
// retaining a line's body. A complete line is classified, the class is kept
// until the blank line, and the body is dropped. Later events are still
// counted. The gateway classifies the finished event NoteEvent already holds,
// so a line of this size is counted here too.
const maxChatFrameEventScan = 1 << 20

// NoteEvent classifies one finished SSE event with the same predicate the
// executor used to schedule it. Write has already hashed these bytes. A line
// longer than the executor's scan cap is still classified: the reader already
// has the finished event.
func (v *ChatFrameVerifier) NoteEvent(ev []byte) {
	if v == nil || len(ev) == 0 {
		return
	}
	v.notedBytes += uint64(len(ev))
	sc := scanSSE(ev)
	if sc.finish {
		v.sawFinishReason = true
	}
	if sc.failed {
		v.sawError = true
	}
	if sc.kind == sseContent {
		v.contentTokens++
	}
	// [DONE] with no finish_reason is a bad close. An error object is its
	// own close, so that stream is not this fault.
	if sc.done && !v.sawFinishReason && !v.sawError {
		v.check.DoneWithoutFinish = true
	}
	v.settleCounts()
}

func (v *ChatFrameVerifier) noteVerifiedCount(claimed, at uint64) {
	v.check.TokenCounts = append(v.check.TokenCounts, claimed)
	if v.check.TokenCountMismatch {
		return
	}
	v.pendingCounts = append(v.pendingCounts, pendingTokenCount{at: at, claimed: claimed})
	v.settleCounts()
}

func (v *ChatFrameVerifier) settleCounts() {
	for len(v.pendingCounts) > 0 && v.notedBytes >= v.pendingCounts[0].at {
		c := v.pendingCounts[0]
		v.pendingCounts = v.pendingCounts[1:]
		v.check.TokenCount = c.claimed
		v.check.ContentTokens = v.contentTokens
		v.check.TokenCountMismatch = c.claimed != v.contentTokens
		if v.check.TokenCountMismatch {
			v.pendingCounts = nil
			return
		}
	}
}

// Close reports a signature whose bytes never arrived, and whether the key
// that signed the stream may sign for the executor.
func (v *ChatFrameVerifier) Close() {
	if v.closed {
		return
	}
	v.closed = true
	// A trailing partial event is not a content token. The events NoteEvent
	// already classified are the whole count.
	v.notedBytes = v.streamBytes
	v.settleCounts()
	if len(v.pending) > 0 {
		v.fail("the stream ended at %d bytes, before the %d a signature covers", v.streamBytes, v.pending[0].at)
	}
	// Two candidates are left only after a single signature, or when the
	// second did not verify under either; the latter already failed, and
	// its candidates are keys no one signed with.
	if len(v.candidates) == 0 || (len(v.candidates) > 1 && v.check.Invalid != "") {
		return
	}
	for _, key := range v.candidates {
		if addr, err := signing.AddressFromPubKey(key); err == nil && v.authorized != nil && v.authorized(addr) {
			v.check.Signed = v.verified
			return
		}
	}
	v.fail("the signer is not the executor")
}

func (v *ChatFrameVerifier) checkReached() {
	for len(v.pending) > 0 && v.check.Invalid == "" && v.pending[0].at == v.streamBytes {
		p := v.pending[0]
		v.pending = v.pending[1:]
		v.verify(p)
	}
}

func (v *ChatFrameVerifier) verify(p pendingFrameSig) {
	preimage, err := chatFramePreimage(v.escrowID, v.inferenceID, v.stream.Sum(nil), p.tokens)
	if err != nil {
		v.fail("%v", err)
		return
	}
	if v.candidates == nil {
		v.candidates = signing.RecoverCompactPubKeys(preimage, p.sig)
		if len(v.candidates) == 0 {
			v.fail("frame %d: no key recovers from it", p.frame)
			return
		}
		v.verified++
		v.noteVerifiedCount(p.tokens, p.at)
		return
	}
	var kept [][]byte
	for _, key := range v.candidates {
		if signing.VerifyCompact(key, preimage, p.sig) {
			kept = append(kept, key)
		}
	}
	if len(kept) == 0 {
		v.fail("frame %d: it does not match the stream", p.frame)
		return
	}
	v.candidates = kept
	v.verified++
	v.noteVerifiedCount(p.tokens, p.at)
}

func (v *ChatFrameVerifier) fail(format string, args ...any) {
	if v.check.Invalid == "" {
		v.check.Invalid = fmt.Sprintf(format, args...)
	}
	v.pending = nil
}

// Check is the result. Signed is counted once Close has identified the
// signer.
func (v *ChatFrameVerifier) Check() host.ChatFrameCheck {
	return v.check
}

type sseKind int

const (
	sseIgnore sseKind = iota
	sseContent
	sseTerminal
	sseProtocol
	// sseFinish is a finish_reason or usage event with no content token.
	// It is held until [DONE] and written into that same tail.
	sseFinish
)

// sseClass is one finished SSE event. finish is a non-empty finish_reason.
// done is [DONE]. failed is an error object.
type sseClass struct {
	kind   sseKind
	finish bool
	done   bool
	failed bool
}

// sseFold is the class of an event built one complete line at a time.
// active means some of those lines were already sent and dropped. carryNewline
// means the dropped bytes ended on '\n', so the next '\n' ends the event.
// stop means a terminal line ended the classification.
type sseFold struct {
	class        sseClass
	sawFinish    bool
	active       bool
	carryNewline bool
	stop         bool
}

// resolved applies the finish-without-content rule once the event has ended.
func (f sseFold) resolved() sseClass {
	if f.stop {
		return f.class
	}
	out := f.class
	if out.kind == sseContent {
		return out
	}
	if f.sawFinish {
		out.kind = sseFinish
	}
	return out
}

// classifySSE reads one finished SSE event. A content token is a non-empty
// content, reasoning, or tool-call delta, or a data line that is not JSON.
// [DONE] and an error object are terminal. A finish_reason or usage object
// with no content token is sseFinish: it joins [DONE] and is not a token.
// JSON with no choices and no usage (the receipt, devshard_meta) is protocol
// and stays in the stream. A role-only delta, an empty delta, and a comment
// are ignored: they do not join, sign, or count.
func classifySSE(event []byte) sseKind {
	return scanSSE(event).kind
}

func scanSSE(event []byte) sseClass {
	var f sseFold
	for _, line := range strings.Split(string(event), "\n") {
		f = foldSSELine(f, line)
		if f.stop {
			return f.class
		}
	}
	return f.resolved()
}

// foldSSELine updates the class with one SSE line. A later line can still
// change it: content wins over finish. The line itself is not retained.
func foldSSELine(f sseFold, line string) sseFold {
	if f.stop {
		return f
	}
	data, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return f
	}
	data = strings.TrimSpace(data)
	if data == "" {
		return f
	}
	if data == "[DONE]" {
		f.class.kind = sseTerminal
		f.class.done = true
		f.stop = true
		return f
	}
	var chunk struct {
		Error   json.RawMessage `json:"error"`
		Usage   json.RawMessage `json:"usage"`
		Choices []struct {
			FinishReason json.RawMessage `json:"finish_reason"`
			Delta        sseDelta        `json:"delta"`
			Message      sseDelta        `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		f.class.kind = sseContent
		return f
	}
	if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
		f.class.kind = sseTerminal
		f.class.failed = true
		f.stop = true
		return f
	}
	if jsonPresent(chunk.Usage) {
		f.sawFinish = true
	}
	if len(chunk.Choices) == 0 {
		if f.class.kind != sseContent && !f.sawFinish {
			f.class.kind = sseProtocol
		}
		return f
	}
	for _, choice := range chunk.Choices {
		if jsonPresent(choice.FinishReason) {
			f.sawFinish = true
			f.class.finish = true
		}
		if choice.Delta.carriesOutput() || choice.Message.carriesOutput() {
			f.class.kind = sseContent
			break
		}
	}
	return f
}

// jsonPresent reports a JSON value that is there and is not null or "".
func jsonPresent(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != `""`
}

func sseEventContentToken(event []byte) bool {
	return classifySSE(event) == sseContent
}

// ContentTokens counts finished SSE events in p that the signature schedule
// counts as content tokens. A trailing partial event is not a token. One
// joined frame can carry several events, so the count is not the chunk count.
func ContentTokens(p []byte) int {
	n := 0
	for len(p) > 0 {
		i := bytes.Index(p, []byte("\n\n"))
		if i < 0 {
			return n
		}
		if sseEventContentToken(p[:i+2]) {
			n++
		}
		p = p[i+2:]
	}
	return n
}

func sseEventTerminal(event []byte) bool {
	return classifySSE(event) == sseTerminal
}

// sseEventCarriesOutput reports whether a finished SSE event is something the
// client waits for: a content token, an error, or [DONE].
func sseEventCarriesOutput(event []byte) bool {
	return sseEventContentToken(event) || sseEventTerminal(event)
}

type sseDelta struct {
	Content          string          `json:"content"`
	Reasoning        string          `json:"reasoning"`
	ReasoningContent string          `json:"reasoning_content"`
	ToolCalls        json.RawMessage `json:"tool_calls"`
}

func (d sseDelta) carriesOutput() bool {
	if d.Content != "" || d.Reasoning != "" || d.ReasoningContent != "" {
		return true
	}
	calls := strings.TrimSpace(string(d.ToolCalls))
	return calls != "" && calls != "null" && calls != "[]"
}
