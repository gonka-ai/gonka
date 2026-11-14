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
	DefaultChatFrameJoin = 5
	// DefaultChatFrameSignEvery is how many chunks share one signature.
	DefaultChatFrameSignEvery = 13
	// MaxChatFrameJoin bounds a gateway's request. A joined group holds tokens
	// until it fills, and the gateway cancels a winner that stays silent for its
	// inter-chunk stall timeout, so the group has to fill well inside it.
	MaxChatFrameJoin = 64
	// MaxChatFrameSignEvery bounds how many chunks can pass between signatures.
	// The interval does not hold tokens.
	MaxChatFrameSignEvery = 4096
)

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
	escrowID       string
	inferenceID    uint64
	authorized     func(address string) bool
	stream         hash.Hash
	streamBytes    uint64
	candidates     [][]byte
	verified       uint64
	pending        []pendingFrameSig
	lastAt         uint64
	check          host.ChatFrameCheck
	closed         bool
	event          []byte
	contentTokens  uint64
	countAbandoned bool
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
// each byte a pending signature names. The same bytes are scanned for content
// tokens, so a verified token_count can be compared with the prefix it signs.
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
	v.noteContent(p)
}

// maxChatFrameEventScan bounds the unfinished SSE event kept while counting
// content tokens. Past it the count is abandoned rather than held in memory.
const maxChatFrameEventScan = 1 << 20

func (v *ChatFrameVerifier) noteContent(p []byte) {
	if v.countAbandoned || len(p) == 0 {
		return
	}
	v.event = append(v.event, p...)
	for {
		i := bytes.Index(v.event, []byte("\n\n"))
		if i < 0 {
			if len(v.event) > maxChatFrameEventScan {
				v.event = nil
				v.countAbandoned = true
			}
			return
		}
		ev := v.event[:i+2]
		v.event = v.event[i+2:]
		if len(v.event) == 0 {
			v.event = nil
		}
		if sseEventContentToken(ev) {
			v.contentTokens++
		}
	}
}

func (v *ChatFrameVerifier) noteVerifiedCount(claimed uint64) {
	v.check.TokenCounts = append(v.check.TokenCounts, claimed)
	if v.countAbandoned || v.check.TokenCountMismatch {
		return
	}
	v.check.TokenCount = claimed
	v.check.ContentTokens = v.contentTokens
	v.check.TokenCountMismatch = claimed != v.contentTokens
}

// Close reports a signature whose bytes never arrived, and whether the key
// that signed the stream may sign for the executor.
func (v *ChatFrameVerifier) Close() {
	if v.closed {
		return
	}
	v.closed = true
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
		v.noteVerifiedCount(p.tokens)
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
	v.noteVerifiedCount(p.tokens)
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
)

// classifySSE reads one finished SSE event. A content token is a non-empty
// content, reasoning, or tool-call delta, or a data line that is not JSON.
// [DONE] and an error object are terminal. JSON with no choices (the receipt,
// devshard_meta) is protocol and stays in the stream. A role-only delta, an
// empty delta, and a comment are ignored: they do not join, sign, or count.
func classifySSE(event []byte) sseKind {
	kind := sseIgnore
	for _, line := range strings.Split(string(event), "\n") {
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			return sseTerminal
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Delta   sseDelta `json:"delta"`
				Message sseDelta `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			kind = sseContent
			continue
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return sseTerminal
		}
		if len(chunk.Choices) == 0 {
			if kind != sseContent {
				kind = sseProtocol
			}
			continue
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.carriesOutput() || choice.Message.carriesOutput() {
				kind = sseContent
				break
			}
		}
	}
	return kind
}

func sseEventContentToken(event []byte) bool {
	return classifySSE(event) == sseContent
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
