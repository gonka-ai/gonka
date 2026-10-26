package transport

import (
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

// DefaultChatFrameEvery is how many finished SSE events share one signature,
// and in the joined schedule one flush, when the gateway asks for nothing else.
const DefaultChatFrameEvery = 5

// maxChatFrameEvery bounds a gateway's request. A joined group holds tokens
// until it fills, and the gateway cancels a winner that stays silent for its
// inter-chunk stall timeout, so the group has to fill well inside it.
const maxChatFrameEvery = 64

func chatFrameEvery(s *host.ChatFrameSchedule) int {
	if s == nil || s.Every == 0 {
		return DefaultChatFrameEvery
	}
	return int(min(s.Every, maxChatFrameEvery))
}

func chatFramesJoined(s *host.ChatFrameSchedule) bool {
	return s == nil || !s.Separate
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

func chatFramePreimage(escrowID string, inferenceID, seq, streamBytes uint64, streamHash []byte) ([]byte, error) {
	return types.CanonicalSignedBytes(&types.ChatFrameSignatureContent{
		EscrowId:    escrowID,
		InferenceId: inferenceID,
		Seq:         seq,
		StreamHash:  streamHash,
		StreamBytes: streamBytes,
	})
}

// ChatFrameSigner signs the uncompressed prefix of one Chat stream. Every
// signature covers the whole prefix, so a cut stream needs only the newest one.
type ChatFrameSigner struct {
	signer      signing.Signer
	escrowID    string
	inferenceID uint64
	stream      hash.Hash
	streamBytes uint64
	signedBytes uint64
	seq         uint64
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

// sign returns the compact signature over the prefix so far, and the number
// of uncompressed bytes it covers.
func (s *ChatFrameSigner) sign() ([]byte, uint64, error) {
	s.seq++
	preimage, err := chatFramePreimage(s.escrowID, s.inferenceID, s.seq, s.streamBytes, s.stream.Sum(nil))
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
	escrowID    string
	inferenceID uint64
	authorized  func(address string) bool
	stream      hash.Hash
	streamBytes uint64
	candidates  [][]byte
	verified    uint64
	pending     []pendingFrameSig
	lastAt      uint64
	check       host.ChatFrameCheck
	seq         uint64
	closed      bool
}

type pendingFrameSig struct {
	seq, at uint64
	sig     []byte
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

// Frame records one received frame and its signature, if it has one.
func (v *ChatFrameVerifier) Frame(sig []byte, streamBytes uint64) {
	v.check.Frames++
	if len(sig) == 0 {
		return
	}
	v.seq++
	if v.check.Invalid != "" {
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
	v.pending = append(v.pending, pendingFrameSig{seq: v.seq, at: streamBytes, sig: sig})
	v.checkReached()
}

// Write folds decompressed stream bytes into the prefix hash, stopping at
// each byte a pending signature names.
func (v *ChatFrameVerifier) Write(p []byte) {
	for len(p) > 0 {
		if len(v.pending) == 0 || v.check.Invalid != "" {
			v.stream.Write(p)
			v.streamBytes += uint64(len(p))
			return
		}
		take := min(uint64(len(p)), v.pending[0].at-v.streamBytes)
		v.stream.Write(p[:take])
		v.streamBytes += take
		p = p[take:]
		v.checkReached()
	}
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
	preimage, err := chatFramePreimage(v.escrowID, v.inferenceID, p.seq, p.at, v.stream.Sum(nil))
	if err != nil {
		v.fail("%v", err)
		return
	}
	if v.candidates == nil {
		v.candidates = signing.RecoverCompactPubKeys(preimage, p.sig)
		if len(v.candidates) == 0 {
			v.fail("signature %d: no key recovers from it", p.seq)
			return
		}
		v.verified++
		return
	}
	var kept [][]byte
	for _, key := range v.candidates {
		if signing.VerifyCompact(key, preimage, p.sig) {
			kept = append(kept, key)
		}
	}
	if len(kept) == 0 {
		v.fail("signature %d: it does not match the stream", p.seq)
		return
	}
	v.candidates = kept
	v.verified++
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

// sseEventCarriesOutput reports whether a finished SSE event is something the
// client waits for: a delta with content, reasoning or tool calls, an error,
// [DONE], or a data line that is not JSON. A role-only delta and protocol
// events are not.
func sseEventCarriesOutput(event []byte) bool {
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
			return true
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Delta   sseDelta `json:"delta"`
				Message sseDelta `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return true
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return true
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.carriesOutput() || choice.Message.carriesOutput() {
				return true
			}
		}
	}
	return false
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
