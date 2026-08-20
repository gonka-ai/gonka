package signedhttp

import (
	"net/http"
	"sync"
	"time"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/utils/httpx"
)

var errReplayedRequest = shared.New("REPLAYED_REQUEST", shared.ErrUnauthorized, "request id has already been served; sign a new request rather than sending one twice")

// Once serves a signed request id one time. It belongs on anything a repeat would act on twice,
// a shell above all, where the answer cannot be recorded and handed back the way a command's is.
// Remembering ids for as long as a signature stays fresh is enough: an older repeat is already
// turned away by its timestamp
type Once struct {
	mu     sync.Mutex
	clock  ports.Clock
	window time.Duration
	served map[string]time.Time
}

func NewOnce(clock ports.Clock, window time.Duration) *Once {
	return &Once{clock: clock, window: window, served: map[string]time.Time{}}
}

func (o *Once) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := RequestIDFrom(r.Context())
		if !o.first(string(AddressFrom(r.Context())) + "/" + requestID) {
			httpx.WriteError(w, requestID, errReplayedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (o *Once) first(request string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	now := o.clock.Now()
	for served, at := range o.served {
		if now.Sub(at) > o.window {
			delete(o.served, served)
		}
	}
	if _, found := o.served[request]; found {
		return false
	}
	o.served[request] = now
	return true
}
