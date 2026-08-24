package signedhttp

import (
	"context"
	"net/http"

	"trainshard/internal/domain/shared"
	"trainshard/internal/utils/httpx"
)

var errReplayedRequest = shared.New("REPLAYED_REQUEST", shared.ErrUnauthorized, "request id has already been served; sign a new request rather than sending one twice")

// Served is spent request ids. It outlives the process: a daemon that only remembers in memory
// serves a captured request again after a restart
type Served interface {
	// First is true the first time this id is seen. A write that does not land must not report true
	First(ctx context.Context, request string) (bool, error)
}

// Once belongs on anything a repeat would act on twice — a shell, where the answer cannot be
// recorded and handed back the way a command's is
type Once struct {
	served Served
}

func NewOnce(served Served) *Once {
	return &Once{served: served}
}

func (o *Once) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := RequestIDFrom(r.Context())
		first, err := o.served.First(r.Context(), string(AddressFrom(r.Context()))+"/"+requestID)
		if err != nil {
			httpx.WriteError(w, requestID, err)
			return
		}
		if !first {
			httpx.WriteError(w, requestID, errReplayedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
