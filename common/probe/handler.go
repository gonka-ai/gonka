package probe

import (
	"net/http"
	"strconv"
	"time"
)

// Handler serves the clock contract: 204 + X-Server-Recv-Ns / X-Server-Send-Ns
// (unix nanoseconds). Allocation-light; no body.
func Handler(now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		recv := now()
		send := now()
		h := w.Header()
		h.Set(HeaderServerRecvNs, strconv.FormatInt(recv.UnixNano(), 10))
		h.Set(HeaderServerSendNs, strconv.FormatInt(send.UnixNano(), 10))
		w.WriteHeader(http.StatusNoContent)
	})
}
