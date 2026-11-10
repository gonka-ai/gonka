package probe

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Unix ms for ~2001..~2286. Values in this band in an *_unix_ns / *-Ns field
// are almost certainly milliseconds mislabeled as nanoseconds.
const (
	unixMilliMin int64 = 1_000_000_000_000      // ~2001-09-09
	unixMilliMax int64 = 10_000_000_000_000     // ~2286-11-20
)

type pingJSON struct {
	RecvUnixNs int64 `json:"recv_unix_ns"`
	SendUnixNs int64 `json:"send_unix_ns"`
}

func parsePingTimestamps(h http.Header, body []byte) (recv, send int64, hasRecv, hasSend bool, err error) {
	if v := h.Get(HeaderServerRecvNs); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil {
			return 0, 0, false, false, fmt.Errorf("probe: parse %s: %w", HeaderServerRecvNs, perr)
		}
		if err := rejectMillisAsNanos(n); err != nil {
			return 0, 0, false, false, err
		}
		recv, hasRecv = n, true
	}
	if v := h.Get(HeaderServerSendNs); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil {
			return 0, 0, false, false, fmt.Errorf("probe: parse %s: %w", HeaderServerSendNs, perr)
		}
		if err := rejectMillisAsNanos(n); err != nil {
			return 0, 0, false, false, err
		}
		send, hasSend = n, true
	}
	if hasRecv && hasSend {
		if send < recv {
			return 0, 0, false, false, fmt.Errorf("probe: send_ns < recv_ns")
		}
		return recv, send, true, true, nil
	}
	if hasRecv || hasSend {
		// Partial headers are unusable for four-timestamp math.
		return 0, 0, false, false, fmt.Errorf("probe: incomplete ping timestamp headers")
	}

	if len(body) == 0 || body[0] != '{' {
		return 0, 0, false, false, nil
	}
	var pj pingJSON
	if jerr := json.Unmarshal(body, &pj); jerr != nil {
		return 0, 0, false, false, nil // not JSON ping; ignore
	}
	if pj.RecvUnixNs == 0 && pj.SendUnixNs == 0 {
		return 0, 0, false, false, nil
	}
	if err := rejectMillisAsNanos(pj.RecvUnixNs); err != nil {
		return 0, 0, false, false, err
	}
	if err := rejectMillisAsNanos(pj.SendUnixNs); err != nil {
		return 0, 0, false, false, err
	}
	if pj.SendUnixNs < pj.RecvUnixNs {
		return 0, 0, false, false, fmt.Errorf("probe: send_unix_ns < recv_unix_ns")
	}
	return pj.RecvUnixNs, pj.SendUnixNs, true, true, nil
}

func rejectMillisAsNanos(v int64) error {
	if v >= unixMilliMin && v <= unixMilliMax {
		return fmt.Errorf("probe: timestamp %d looks like unix milliseconds in a nanosecond field", v)
	}
	return nil
}

func parseDateHeader(h http.Header) (time.Time, bool) {
	v := h.Get("Date")
	if v == "" {
		return time.Time{}, false
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
