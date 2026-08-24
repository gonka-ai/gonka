//go:build testenvci

package host

import (
	"net/http"
	"os"
	"strconv"
	"sync/atomic"

	"devshard/logging"
)

func init() {
	if v := os.Getenv("DEVSHARD_TEST_DETACH_PRIMARY_AFTER_WRITES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			testDetachPrimaryAfterWrites.Store(n)
			logging.Warn("test fault injector armed: primary writer will be detached mid-stream",
				"subsystem", "host", "after_writes", n)
		}
	}
}

// testDetachPrimaryAfterWrites, when > 0, tears down each LiveStream's primary
// writer after that many successful drain writes. The reconnect citest uses it
// to inject a gateway↔host drop while ML keeps draining.
var testDetachPrimaryAfterWrites atomic.Int64

// injectPrimaryDetachFault reports whether the primary drain should stop because
// the fault injector fired. Bytes already written count as delivered, so the
// gateway sees a mid-stream drop and can same-nonce reconnect to the live log.
func (s *LiveStream) injectPrimaryDetachFault(w http.ResponseWriter, sub *liveSubscriber) bool {
	limit := testDetachPrimaryAfterWrites.Load()
	if limit <= 0 {
		return false
	}
	if atomic.AddInt64(&s.testPrimaryWrites, 1) < limit {
		return false
	}
	s.clientDetached.Store(true)
	s.unsubscribe(sub)
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			_ = conn.Close()
		}
	}
	return true
}
