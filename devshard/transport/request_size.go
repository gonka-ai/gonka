package transport

import (
	"errors"
	"strconv"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"devshard/types"
)

// MaxHostRequestBytes is the largest body a host-bound request may carry: the cap the public proxy and
// the versiond router enforce, so a body past it is refused on the way whatever a host would accept.
const MaxHostRequestBytes = 10 << 20

// envelopeBytes covers the JSON and protobuf framing around the payload and the diffs, deliberately far above what it costs.
const envelopeBytes = 2 << 10

// promptField is the InferenceRequestEnvelope field the prompt travels in as raw bytes. See MarshalWrappedInferenceRequest.
const promptField protowire.Number = 4

// ErrHostRequestTooLarge reports a request no host can be sent, because the wire format it would take
// is past what the path between here and that host accepts.
var ErrHostRequestTooLarge = errors.New("host request too large")

// base64Bytes is what n raw bytes cost once encoded, which is how every byte slice inside the JSON document travels.
func base64Bytes(n int) int {
	return (n + 2) / 3 * 4
}

// PromptWireBytes is what a prompt costs in a host-bound body, envelope included: raw bytes outside the JSON document.
func PromptWireBytes(promptBytes int) int {
	return protowire.SizeTag(promptField) + protowire.SizeBytes(promptBytes) + envelopeBytes
}

// DiffsExceedWireBytes reports whether diffs cost more than limit on the wire, stopping as soon as they
// do, so the walk is bounded by the limit rather than by the history.
func DiffsExceedWireBytes(diffs []types.Diff, limit int) bool {
	total := 0
	for _, diff := range diffs {
		if total += diffWireBytes(diff); total > limit {
			return true
		}
	}
	return false
}

// diffWireBytes is what one diff costs in a host-bound body: the DiffJSON it travels as, whose txs are its DiffContent bytes, and a separator.
func diffWireBytes(diff types.Diff) int {
	content := proto.Size(&types.DiffContent{Nonce: diff.Nonce, Txs: diff.Txs})
	size := len(`{"nonce":,"txs":,"user_sig":},`) + len(strconv.FormatUint(diff.Nonce, 10)) +
		jsonBytesField(content) + jsonBytesField(len(diff.UserSig))
	if len(diff.PostStateRoot) > 0 {
		size += len(`,"post_state_root":`) + jsonBytesField(len(diff.PostStateRoot))
	}
	return size
}

// jsonBytesField is what n bytes cost as a JSON value: a quoted base64 string, or null when there are none.
func jsonBytesField(n int) int {
	if n == 0 {
		return len("null")
	}
	return base64Bytes(n) + len(`""`)
}

// DiffsWithinWireBytes returns how many leading diffs fit limit on the wire. It returns at least one,
// so a single diff past the limit still moves instead of stalling the walk that carries it.
func DiffsWithinWireBytes(diffs []types.Diff, limit int) int {
	total := 0
	for index, diff := range diffs {
		if total += diffWireBytes(diff); total > limit {
			return max(index, 1)
		}
	}
	return len(diffs)
}

// InferenceRequestFits reports whether a prompt and its catch-up fit one host-bound body.
func InferenceRequestFits(promptBytes int, diffs []types.Diff) bool {
	room := MaxHostRequestBytes - PromptWireBytes(promptBytes)
	if room < 0 {
		return false
	}
	return !DiffsExceedWireBytes(diffs, room)
}
