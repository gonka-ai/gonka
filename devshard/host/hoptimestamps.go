package host

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Hop-timestamp wire helpers for Step 5g (gateway-attempt-reconnect-plan.md).
// Comments are injected at the subscriber/cache writer only — never into the
// hashed LiveStream log or response_payload.

const (
	// DevshardTSCommentPrefix is the SSE comment prefix (including trailing space).
	DevshardTSCommentPrefix = ": devshard-ts "

	// MaxDevshardTSEventsPerComment caps one write's content events and the
	// single :devshard-ts comment that precedes them (one comment ↔ one write).
	MaxDevshardTSEventsPerComment = 64

	// MaxDevshardTSCommentBytes bounds a single comment line.
	MaxDevshardTSCommentBytes = 16 << 10 // 16 KiB

	HopTierLive  = "live"
	HopTierCache = "cache"
)

// DevshardTSBatch is the JSON body of a `: devshard-ts` comment.
type DevshardTSBatch struct {
	B  int64   `json:"b"`
	ML []int64 `json:"ml"`
	W  []int64 `json:"w"`
	T  string  `json:"t,omitempty"`
}

// FormatDevshardTSComment builds one SSE comment line for a batch of events.
// Returns "" if the batch is empty, malformed, or would exceed size bounds —
// callers must treat that as "skip comment", never as a stream error.
func FormatDevshardTSComment(b int64, ml, w []int64, tier string) string {
	if len(ml) == 0 || len(ml) != len(w) || len(ml) > MaxDevshardTSEventsPerComment {
		return ""
	}
	payload, err := json.Marshal(DevshardTSBatch{B: b, ML: ml, W: w, T: tier})
	if err != nil {
		return ""
	}
	line := DevshardTSCommentPrefix + string(payload)
	if len(line) > MaxDevshardTSCommentBytes {
		return ""
	}
	return line
}

// AppendDevshardTSComment appends at most one comment line for ml/w.
// Callers must pass len(ml) ≤ MaxDevshardTSEventsPerComment (one write ↔ one
// comment). Oversized or malformed batches are skipped (dst unchanged).
func AppendDevshardTSComment(dst []byte, baseB int64, ml, w []int64, tier string) []byte {
	line := FormatDevshardTSComment(baseB, ml, w, tier)
	if line == "" {
		return dst
	}
	dst = append(dst, line...)
	dst = append(dst, '\n')
	return dst
}

// FreshWriteTimes returns len(n) host wall-ms stamps for this connection emit.
func FreshWriteTimes(n int) []int64 {
	if n <= 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	out := make([]int64, n)
	for i := range out {
		out[i] = now
	}
	return out
}

// ParseDevshardTSComment parses a full SSE comment line. ok is false for
// non-matching, malformed, or oversized input (never an error).
func ParseDevshardTSComment(line string) (DevshardTSBatch, bool) {
	if !strings.HasPrefix(line, DevshardTSCommentPrefix) {
		// Also accept ":devshard-ts " without the conventional space after ':'?
		// Spec example uses ": devshard-ts "; keep strict.
		return DevshardTSBatch{}, false
	}
	raw := strings.TrimSpace(strings.TrimPrefix(line, DevshardTSCommentPrefix))
	if raw == "" || len(raw) > MaxDevshardTSCommentBytes {
		return DevshardTSBatch{}, false
	}
	var batch DevshardTSBatch
	if err := json.Unmarshal([]byte(raw), &batch); err != nil {
		return DevshardTSBatch{}, false
	}
	if len(batch.ML) == 0 || len(batch.ML) != len(batch.W) || len(batch.ML) > MaxDevshardTSEventsPerComment {
		return DevshardTSBatch{}, false
	}
	if batch.B < 0 {
		return DevshardTSBatch{}, false
	}
	return batch, true
}

// FormatDevshardTSCommentLine is a helper for tests / replay that returns the
// comment including a trailing newline, or empty on skip.
func FormatDevshardTSCommentLine(b int64, ml, w []int64, tier string) string {
	line := FormatDevshardTSComment(b, ml, w, tier)
	if line == "" {
		return ""
	}
	return line + "\n"
}

// debug string for logs
func (b DevshardTSBatch) String() string {
	return fmt.Sprintf("b=%d n=%d t=%s", b.B, len(b.ML), b.T)
}
